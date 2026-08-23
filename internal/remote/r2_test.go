package remote

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type s3Fake struct {
	key     string
	body    []byte
	listed  bool
	got     bool
	deleted bool
	objects map[string][]byte
}

func (f *s3Fake) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (f *s3Fake) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listed = true
	output := &s3.ListObjectsV2Output{}
	for key, data := range f.objects {
		if strings.HasPrefix(key, aws.ToString(input.Prefix)) {
			output.Contents = append(output.Contents, types.Object{Key: aws.String(key), Size: aws.Int64(int64(len(data)))})
		}
	}
	return output, nil
}

func (f *s3Fake) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.key = aws.ToString(input.Key)
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	f.body = data
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	f.objects[f.key] = data
	return &s3.PutObjectOutput{}, nil
}

func (f *s3Fake) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.got = true
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.objects[aws.ToString(input.Key)]))}, nil
}

func (f *s3Fake) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleted = true
	delete(f.objects, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func TestR2PrefixesObjectsWithSchemaVersion(t *testing.T) {
	t.Parallel()
	client := &s3Fake{}
	r2 := &R2{client: client, bucket: "bucket", prefix: "saveknot", identity: "account/bucket/saveknot"}
	if err := r2.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !client.listed || !client.got || !client.deleted || len(client.objects) != 0 {
		t.Fatalf("capability test did not exercise and clean up all operations: %#v", client)
	}
	if err := r2.Put(context.Background(), "/games/a.json", "application/json", "", bytes.NewReader([]byte("{}")), 2); err != nil {
		t.Fatal(err)
	}
	if client.key != "saveknot/v1/games/a.json" || string(client.body) != "{}" {
		t.Fatalf("unexpected upload: key=%q body=%q", client.key, client.body)
	}
	objects, err := r2.List(context.Background(), "games/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != "games/a.json" || objects[0].Size != 2 {
		t.Fatalf("unexpected object listing: %#v", objects)
	}
	if r2.Identity() != "account/bucket/saveknot" {
		t.Fatalf("unexpected R2 identity: %q", r2.Identity())
	}
	r2.prefix = ""
	if got := r2.key("blobs/a"); got != "v1/blobs/a" {
		t.Fatalf("unexpected root prefix: %q", got)
	}
}
