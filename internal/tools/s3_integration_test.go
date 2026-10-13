package tools

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestS3Integration exercises the full request path (SigV4 signing + HTTP + XML
// parsing) against a real S3-compatible endpoint. It is skipped unless
// S3_TEST_ENDPOINT is set, so it never runs in a normal build:
//
//	S3_TEST_ENDPOINT=http://127.0.0.1:9100 S3_TEST_ACCESS=… S3_TEST_SECRET=… \
//	  go test ./internal/tools/ -run TestS3Integration -v
//
// The bucket must already exist (bucket creation is out of the connector's scope).
func TestS3Integration(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set S3_TEST_ENDPOINT to run the S3 integration test")
	}
	cfg := map[string]string{
		"endpoint":   endpoint,
		"region":     "us-east-1",
		"access_key": os.Getenv("S3_TEST_ACCESS"),
		"secret_key": os.Getenv("S3_TEST_SECRET"),
	}
	bucket := os.Getenv("S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "verify"
	}
	ctx := context.Background()

	if _, err := s3ListBuckets(ctx, cfg); err != nil {
		t.Fatalf("list buckets: %v", err)
	}

	src := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(src, []byte("hello s3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s3PutObject(ctx, cfg, bucket, "dir/hello.txt", src, false); err != nil {
		t.Fatalf("put: %v", err)
	}

	res, err := s3ListObjects(ctx, cfg, bucket, "dir/", 100)
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].Key != "dir/hello.txt" {
		t.Fatalf("unexpected listing: %+v", res.Contents)
	}

	dst := filepath.Join(t.TempDir(), "out.txt")
	if _, err := s3GetObject(ctx, cfg, bucket, "dir/hello.txt", dst); err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "hello s3\n" {
		t.Fatalf("roundtrip mismatch: %q", got)
	}

	// The presigned URL must be usable by a plain HTTP client (no signing headers).
	url, err := s3Presign(cfg, "GET", bucket, "dir/hello.txt", 300)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("presigned GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("presigned GET status %d", resp.StatusCode)
	}

	// Default-bucket path: with cfg["bucket"] set, Test validates via ListObjects and
	// Dashboard previews the bucket's objects (not the bucket list).
	scoped := map[string]string{}
	for k, v := range cfg {
		scoped[k] = v
	}
	scoped["bucket"] = bucket
	if err := (s3c{}).Test(ctx, scoped); err != nil {
		t.Fatalf("Test with default bucket: %v", err)
	}
	dash, err := (s3c{}).Dashboard(ctx, scoped)
	if err != nil {
		t.Fatalf("Dashboard with default bucket: %v", err)
	}
	if d, ok := dash.(s3Dashboard); !ok || d.Bucket != bucket {
		t.Fatalf("Dashboard did not preview the configured bucket: %+v", dash)
	}

	if err := s3DeleteObject(ctx, cfg, bucket, "dir/hello.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
