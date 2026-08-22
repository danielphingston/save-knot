package remote

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/saveknot/saveknot/internal/config"
)

type s3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type R2 struct {
	client   s3API
	bucket   string
	prefix   string
	identity string
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
		return fmt.Errorf("connect to R2 bucket %q: %w", r.bucket, err)
	}
	return nil
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

func (r *R2) key(key string) string {
	key = strings.TrimLeft(key, "/")
	if r.prefix == "" {
		return "v1/" + key
	}
	return r.prefix + "/v1/" + key
}
