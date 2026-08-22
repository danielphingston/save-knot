package remote

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3Fake struct {
	key  string
	body []byte
}

func (f *s3Fake) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (f *s3Fake) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.key = aws.ToString(input.Key)
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	f.body = data
	return &s3.PutObjectOutput{}, nil
}

func TestR2PrefixesObjectsWithSchemaVersion(t *testing.T) {
	t.Parallel()
	client := &s3Fake{}
	r2 := &R2{client: client, bucket: "bucket", prefix: "saveknot", identity: "account/bucket/saveknot"}
	if err := r2.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r2.Put(context.Background(), "/games/a.json", "application/json", "", bytes.NewReader([]byte("{}")), 2); err != nil {
		t.Fatal(err)
	}
	if client.key != "saveknot/v1/games/a.json" || string(client.body) != "{}" {
		t.Fatalf("unexpected upload: key=%q body=%q", client.key, client.body)
	}
	if r2.Identity() != "account/bucket/saveknot" {
		t.Fatalf("unexpected R2 identity: %q", r2.Identity())
	}
	r2.prefix = ""
	if got := r2.key("blobs/a"); got != "v1/blobs/a" {
		t.Fatalf("unexpected root prefix: %q", got)
	}
}
