package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/saveknot/saveknot/internal/config"
)

type s3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type R2 struct {
	client   s3API
	bucket   string
	prefix   string
	identity string
}

type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

func NewR2(ctx context.Context, settings config.R2, secret string) (*R2, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("secret access key is required")
	}
	awsConfiguration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(settings.AccessKeyID, secret, "")),
		// R2 does not need the SDK's optional S3 CRC32 streaming checksum. Recent
		// AWS SDK releases enable it by default, which can produce an aws-chunked
		// request that third-party S3 endpoints reject with SignatureDoesNotMatch.
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("configure R2 client: %w", err)
	}
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", settings.AccountID)
	client := s3.NewFromConfig(awsConfiguration, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	identity := strings.Join([]string{settings.AccountID, settings.Bucket, settings.ObjectPrefix()}, "/")
	return &R2{client: client, bucket: settings.Bucket, prefix: settings.ObjectPrefix(), identity: identity}, nil
}

func (r *R2) Identity() string {
	return r.identity
}

func (r *R2) Test(ctx context.Context) error {
	if _, err := r.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(r.bucket)}); err != nil {
		return fmt.Errorf("R2 HeadBucket failed for %q: %w", r.bucket, err)
	}
	if _, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket), Prefix: aws.String(r.key(".capability/")), MaxKeys: aws.Int32(1),
	}); err != nil {
		return fmt.Errorf("R2 ListObjectsV2 failed for %q (token needs object-list permission): %w", r.bucket, err)
	}
	probeKey := fmt.Sprintf(".capability/%d", time.Now().UTC().UnixNano())
	want := []byte("saveknot-r2-capability-check")
	if err := r.Put(ctx, probeKey, "application/octet-stream", "", bytes.NewReader(want), int64(len(want))); err != nil {
		return fmt.Errorf("R2 write capability check failed: %w", err)
	}
	fullKey := r.key(probeKey)
	output, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(fullKey)})
	if err != nil {
		return errors.Join(fmt.Errorf("R2 GetObject capability check failed: %w", err), r.deleteProbe(ctx, fullKey))
	}
	got, readErr := io.ReadAll(io.LimitReader(output.Body, int64(len(want)+1)))
	closeErr := output.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return errors.Join(fmt.Errorf("read R2 capability object: %w", err), r.deleteProbe(ctx, fullKey))
	}
	if !bytes.Equal(got, want) {
		return errors.Join(errors.New("R2 capability object contents did not match the upload"), r.deleteProbe(ctx, fullKey))
	}
	if err := r.deleteProbe(ctx, fullKey); err != nil {
		return fmt.Errorf("R2 DeleteObject capability check failed (token needs object-delete permission): %w", err)
	}
	return nil
}

func (r *R2) deleteProbe(ctx context.Context, key string) error {
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	return err
}

func (r *R2) Put(ctx context.Context, key, contentType, encoding string, body io.Reader, size int64) error {
	input := &s3.PutObjectInput{
		Bucket:        aws.String(r.bucket),
		Key:           aws.String(r.key(key)),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	}
	if encoding != "" {
		input.ContentEncoding = aws.String(encoding)
	}
	if _, err := r.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("upload R2 object %q: %w", key, err)
	}
	return nil
}

func (r *R2) Delete(ctx context.Context, key string) error {
	fullKey := r.key(key)
	if _, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(fullKey)}); err != nil {
		return fmt.Errorf("delete R2 object %q: %w", key, err)
	}
	return nil
}

func (r *R2) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	output, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(r.key(key))})
	if err != nil {
		return nil, 0, fmt.Errorf("download R2 object %q: %w", key, err)
	}
	return output.Body, aws.ToInt64(output.ContentLength), nil
}

func (r *R2) List(ctx context.Context, prefix string) ([]Object, error) {
	fullPrefix := r.key(prefix)
	base := r.key("")
	var continuation *string
	var objects []Object
	for {
		output, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(r.bucket), Prefix: aws.String(fullPrefix), ContinuationToken: continuation,
		})
		if err != nil {
			return nil, fmt.Errorf("list R2 objects under %q: %w", prefix, err)
		}
		for _, item := range output.Contents {
			key := aws.ToString(item.Key)
			if !strings.HasPrefix(key, base) {
				continue
			}
			objects = append(objects, Object{
				Key: strings.TrimPrefix(key, base), Size: aws.ToInt64(item.Size), LastModified: aws.ToTime(item.LastModified),
			})
		}
		if !aws.ToBool(output.IsTruncated) {
			break
		}
		if output.NextContinuationToken == nil {
			return nil, errors.New("R2 returned a truncated object list without a continuation token")
		}
		continuation = output.NextContinuationToken
	}
	return objects, nil
}

func (r *R2) key(key string) string {
	key = strings.TrimLeft(key, "/")
	if r.prefix == "" {
		return "v1/" + key
	}
	return r.prefix + "/v1/" + key
}
