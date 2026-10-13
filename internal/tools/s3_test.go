package tools

import "testing"

// awsExampleSecret is the well-known secret key from the AWS SigV4 documentation
// examples (paired with access key AKIAIOSFODNN7EXAMPLE). Not a real credential.
const awsExampleSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

// TestS3SignatureVectors checks the SigV4 core against the published AWS S3 examples
// (https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html).
func TestS3SignatureVectors(t *testing.T) {
	const (
		region    = "us-east-1"
		dateStamp = "20130524"
		amzDate   = "20130524T000000Z"
		host      = "examplebucket.s3.amazonaws.com"
	)
	cases := []struct {
		name         string
		method       string
		canonicalURI string
		canonQuery   string
		signed       []string
		values       map[string]string
		payloadHash  string
		want         string
	}{
		{
			name:         "GET object with range",
			method:       "GET",
			canonicalURI: "/test.txt",
			signed:       []string{"host", "range", "x-amz-content-sha256", "x-amz-date"},
			values: map[string]string{
				"host": host, "range": "bytes=0-9",
				"x-amz-content-sha256": emptyPayloadHash, "x-amz-date": amzDate,
			},
			payloadHash: emptyPayloadHash,
			want:        "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name:         "PUT object with encoded key",
			method:       "PUT",
			canonicalURI: "/test%24file.text",
			signed:       []string{"date", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-storage-class"},
			values: map[string]string{
				"date": "Fri, 24 May 2013 00:00:00 GMT", "host": host,
				"x-amz-content-sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
				"x-amz-date":           amzDate, "x-amz-storage-class": "REDUCED_REDUNDANCY",
			},
			payloadHash: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			want:        "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name:         "GET bucket lifecycle",
			method:       "GET",
			canonicalURI: "/",
			canonQuery:   "lifecycle=",
			signed:       []string{"host", "x-amz-content-sha256", "x-amz-date"},
			values: map[string]string{
				"host": host, "x-amz-content-sha256": emptyPayloadHash, "x-amz-date": amzDate,
			},
			payloadHash: emptyPayloadHash,
			want:        "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name:         "list objects",
			method:       "GET",
			canonicalURI: "/",
			canonQuery:   "max-keys=2&prefix=J",
			signed:       []string{"host", "x-amz-content-sha256", "x-amz-date"},
			values: map[string]string{
				"host": host, "x-amz-content-sha256": emptyPayloadHash, "x-amz-date": amzDate,
			},
			payloadHash: emptyPayloadHash,
			want:        "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got := s3signature(awsExampleSecret, region, dateStamp, amzDate, c.method,
				c.canonicalURI, c.canonQuery, c.signed, c.values, c.payloadHash)
			if got != c.want {
				t.Errorf("signature = %s, want %s", got, c.want)
			}
		})
	}
}

func TestS3CanonicalQuery(t *testing.T) {
	cases := []struct {
		in   map[string]string
		want string
	}{
		{nil, ""},
		{map[string]string{"max-keys": "2", "prefix": "J"}, "max-keys=2&prefix=J"},
		{map[string]string{"lifecycle": ""}, "lifecycle="},
		// Keys sorted after encoding; spaces become %20, not '+'.
		{map[string]string{"prefix": "a b", "list-type": "2"}, "list-type=2&prefix=a%20b"},
	}
	for _, c := range cases {
		if got := s3CanonicalQuery(c.in); got != c.want {
			t.Errorf("s3CanonicalQuery(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestS3URIEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"test$file.text", false, "test%24file.text"},
		{"foo/bar baz", false, "foo/bar%20baz"},
		{"foo/bar", true, "foo%2Fbar"},
		{"a~b.c-d_e", true, "a~b.c-d_e"},
		{"é", true, "%C3%A9"}, // UTF-8 bytes encoded individually
	}
	for _, c := range cases {
		if got := s3URIEncode(c.in, c.encodeSlash); got != c.want {
			t.Errorf("s3URIEncode(%q, %v) = %q, want %q", c.in, c.encodeSlash, got, c.want)
		}
	}
}

func TestS3Target(t *testing.T) {
	pathStyle := map[string]string{"endpoint": "https://minio.local:9000"}
	host, uri, scheme, err := s3target(pathStyle, "bk", "dir/obj.txt")
	if err != nil {
		t.Fatal(err)
	}
	if host != "minio.local:9000" || uri != "/bk/dir/obj.txt" || scheme != "https" {
		t.Errorf("path-style: host=%s uri=%s scheme=%s", host, uri, scheme)
	}

	virtual := map[string]string{"endpoint": "https://s3.amazonaws.com", "virtual_host": "true"}
	host, uri, _, err = s3target(virtual, "examplebucket", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if host != "examplebucket.s3.amazonaws.com" || uri != "/test.txt" {
		t.Errorf("virtual-hosted: host=%s uri=%s", host, uri)
	}

	// Service root (ListBuckets): no bucket.
	_, uri, _, err = s3target(pathStyle, "", "")
	if err != nil || uri != "/" {
		t.Errorf("service root: uri=%s err=%v", uri, err)
	}
}
