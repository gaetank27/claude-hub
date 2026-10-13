package tools

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() { register(s3c{}) }

// s3c is a generic connector for any S3-compatible object store (AWS S3, MinIO,
// Garage, Backblaze B2, …). Requests are authenticated with AWS Signature Version 4
// computed in-process (the project stays dependency-light — no AWS SDK). Path-style
// addressing is the default (endpoint/bucket/key) since self-hosted gateways like
// MinIO require it; virtual-hosted addressing (bucket.endpoint) is opt-in.
type s3c struct{}

const (
	s3Service = "s3"
	// emptyPayloadHash is Hex(SHA256("")), used as x-amz-content-sha256 for bodyless
	// requests (GET/DELETE/list).
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayload  = "UNSIGNED-PAYLOAD"
	amzDateFmt       = "20060102T150405Z"
	dateStampFmt     = "20060102"
)

func (s3c) Type() string  { return "s3" }
func (s3c) Label() string { return "S3 (stockage objet)" }

func (s3c) Fields() []Field {
	return []Field{
		{Key: "endpoint", Label: "Endpoint", Type: FieldURL, Required: true,
			Placeholder: "https://s3.amazonaws.com"},
		{Key: "region", Label: "Région", Type: FieldText, Placeholder: "us-east-1"},
		{Key: "access_key", Label: "Access key", Type: FieldText, Required: true},
		{Key: "secret_key", Label: "Secret key", Type: FieldPassword, Secret: true, Required: true},
		{Key: "bucket", Label: "Bucket par défaut (optionnel)", Type: FieldText,
			Placeholder: "mon-bucket"},
		{Key: "virtual_host", Label: "Adressage virtual-hosted (bucket.endpoint) au lieu de path-style", Type: FieldBool},
		{Key: "skip_verify", Label: "Ne pas vérifier le certificat TLS (serveur self-signed)", Type: FieldBool},
	}
}

func (s3c) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "endpoint", "access_key", "secret_key"); err != nil {
		return err
	}
	// Credentials scoped to a single bucket usually lack ListAllMyBuckets, so when a
	// default bucket is set validate against it (ListObjects) rather than the service.
	if b := cfg["bucket"]; b != "" {
		_, err := s3ListObjects(ctx, cfg, b, "", 1)
		return err
	}
	_, err := s3ListBuckets(ctx, cfg)
	return err
}

// s3bucket resolves the bucket for an action: the explicit argument wins, otherwise
// the connector's configured default bucket.
func s3bucket(cfg map[string]string, arg string) string {
	if arg != "" {
		return arg
	}
	return cfg["bucket"]
}

func (s3c) Tools() []ToolSpec {
	bucket := ArgSpec{Name: "bucket", Type: "string",
		Description: "Bucket name (defaults to the connector's configured bucket if set)"}
	key := ArgSpec{Name: "key", Type: "string", Description: "Object key", Required: true}
	return []ToolSpec{
		{Name: "s3_list_buckets", Description: "List the buckets available on the configured S3 endpoint."},
		{Name: "s3_list_objects", Description: "List objects in a bucket, optionally filtered by key prefix.",
			Args: []ArgSpec{
				bucket,
				{Name: "prefix", Type: "string", Description: "Only list keys starting with this prefix"},
				{Name: "max", Type: "integer", Description: "Max number of keys to return (default 1000)"},
			}},
		{Name: "s3_get_object", Description: "Download an object to a local file path on the host.",
			Args: []ArgSpec{bucket, key,
				{Name: "dest", Type: "string", Description: "Local destination file path on the host", Required: true}}},
		{Name: "s3_put_object", Description: "Upload a local file from the host to an object key. With public=true the object is stored public-read (anyone can GET it) and the direct, non-presigned URL is returned.",
			Args: []ArgSpec{bucket, key,
				{Name: "file", Type: "string", Description: "Local source file path on the host", Required: true},
				{Name: "public", Type: "boolean", Description: "Make the object world-readable (x-amz-acl: public-read) and return its direct URL. Requires the bucket to allow public object ACLs."}}},
		{Name: "s3_delete_object", Description: "Delete an object from a bucket.", Args: []ArgSpec{bucket, key}},
		{Name: "s3_set_acl", Description: "Change the ACL of an existing object: make it public-read or private again.",
			Args: []ArgSpec{bucket, key,
				{Name: "acl", Type: "string", Description: "Canned ACL: public-read or private. Default public-read"}}},
		{Name: "s3_public_url", Description: "Return the direct, non-presigned URL of an object (endpoint/bucket/key). The object must already be public-read for the URL to be reachable; this does not change the ACL.",
			Args: []ArgSpec{bucket, key}},
		{Name: "s3_presign_url", Description: "Generate a presigned URL for an object (default GET, 1h validity).",
			Args: []ArgSpec{bucket, key,
				{Name: "method", Type: "string", Description: "HTTP method: GET (download) or PUT (upload). Default GET"},
				{Name: "expires", Type: "integer", Description: "Validity in seconds (default 3600)"}}},
	}
}

func (s3c) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	switch action {
	case "s3_list_buckets":
		bs, err := s3ListBuckets(ctx, cfg)
		if err != nil {
			return "", err
		}
		if len(bs) == 0 {
			return "No buckets found.", nil
		}
		var b strings.Builder
		for _, bk := range bs {
			fmt.Fprintf(&b, "- %s (created %s)\n", bk.Name, bk.CreationDate)
		}
		return strings.TrimSpace(b.String()), nil

	case "s3_list_objects":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		if bucket == "" {
			return "", fmt.Errorf("bucket is required (none provided and no default configured)")
		}
		max, _ := argInt(args, "max")
		res, err := s3ListObjects(ctx, cfg, bucket, argString(args, "prefix", ""), max)
		if err != nil {
			return "", err
		}
		if len(res.Contents) == 0 {
			return "No objects found.", nil
		}
		var b strings.Builder
		for _, o := range res.Contents {
			fmt.Fprintf(&b, "- %s (%s, %s)\n", o.Key, humanBytes(o.Size), o.LastModified)
		}
		if res.IsTruncated {
			b.WriteString("… (truncated; pass a higher max or a narrower prefix)\n")
		}
		return strings.TrimSpace(b.String()), nil

	case "s3_get_object":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		key := argString(args, "key", "")
		dest := argString(args, "dest", "")
		if bucket == "" || key == "" || dest == "" {
			return "", fmt.Errorf("bucket, key and dest are required")
		}
		n, err := s3GetObject(ctx, cfg, bucket, key, dest)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Downloaded s3://%s/%s (%s) to %s", bucket, key, humanBytes(n), dest), nil

	case "s3_put_object":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		key := argString(args, "key", "")
		file := argString(args, "file", "")
		if bucket == "" || key == "" || file == "" {
			return "", fmt.Errorf("bucket, key and file are required")
		}
		public := argBool(args, "public")
		n, err := s3PutObject(ctx, cfg, bucket, key, file, public)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("Uploaded %s (%s) to s3://%s/%s", file, humanBytes(n), bucket, key)
		if public {
			url, uerr := s3PublicURL(cfg, bucket, key)
			if uerr != nil {
				return "", uerr
			}
			out += fmt.Sprintf("\nPublic (public-read), direct URL: %s", url)
		}
		return out, nil

	case "s3_set_acl":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		key := argString(args, "key", "")
		if bucket == "" || key == "" {
			return "", fmt.Errorf("bucket and key are required")
		}
		acl := strings.ToLower(argString(args, "acl", "public-read"))
		if acl != "public-read" && acl != "private" {
			return "", fmt.Errorf("acl must be public-read or private")
		}
		if err := s3SetObjectACL(ctx, cfg, bucket, key, acl); err != nil {
			return "", err
		}
		if acl == "public-read" {
			url, uerr := s3PublicURL(cfg, bucket, key)
			if uerr != nil {
				return "", uerr
			}
			return fmt.Sprintf("Set s3://%s/%s to public-read.\nDirect URL: %s", bucket, key, url), nil
		}
		return fmt.Sprintf("Set s3://%s/%s to private.", bucket, key), nil

	case "s3_public_url":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		key := argString(args, "key", "")
		if bucket == "" || key == "" {
			return "", fmt.Errorf("bucket and key are required")
		}
		return s3PublicURL(cfg, bucket, key)

	case "s3_delete_object":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		key := argString(args, "key", "")
		if bucket == "" || key == "" {
			return "", fmt.Errorf("bucket and key are required")
		}
		if err := s3DeleteObject(ctx, cfg, bucket, key); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted s3://%s/%s", bucket, key), nil

	case "s3_presign_url":
		bucket := s3bucket(cfg, argString(args, "bucket", ""))
		key := argString(args, "key", "")
		if bucket == "" || key == "" {
			return "", fmt.Errorf("bucket and key are required")
		}
		method := strings.ToUpper(argString(args, "method", "GET"))
		if method != "GET" && method != "PUT" {
			return "", fmt.Errorf("method must be GET or PUT")
		}
		expires := 3600
		if e, ok := argInt(args, "expires"); ok && e > 0 {
			expires = e
		}
		return s3Presign(cfg, method, bucket, key, expires)

	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// Dashboard implements Dashboarder: a light snapshot fetched in a single call. With
// a default bucket configured it previews that bucket's objects (creds scoped to one
// bucket often can't ListBuckets); otherwise it lists the buckets.
func (s3c) Dashboard(ctx context.Context, cfg map[string]string) (any, error) {
	if err := missing(cfg, "endpoint", "access_key", "secret_key"); err != nil {
		return nil, err
	}
	if b := cfg["bucket"]; b != "" {
		res, err := s3ListObjects(ctx, cfg, b, "", 20)
		if err != nil {
			return nil, err
		}
		return s3Dashboard{Bucket: b, Count: len(res.Contents), Objects: res.Contents}, nil
	}
	bs, err := s3ListBuckets(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return s3Dashboard{Count: len(bs), Buckets: bs}, nil
}

type s3Dashboard struct {
	Count   int        `json:"count"`
	Buckets []s3Bucket `json:"buckets,omitempty"`
	Bucket  string     `json:"bucket,omitempty"`
	Objects []s3Object `json:"objects,omitempty"`
}

// --- operations ---

type s3Bucket struct {
	Name         string `xml:"Name" json:"name"`
	CreationDate string `xml:"CreationDate" json:"creationDate"`
}

func s3ListBuckets(ctx context.Context, cfg map[string]string) ([]s3Bucket, error) {
	resp, err := s3do(ctx, cfg, http.MethodGet, "", "", nil, nil, emptyPayloadHash, 0, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Buckets struct {
			Bucket []s3Bucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("parse ListBuckets: %w", err)
	}
	return r.Buckets.Bucket, nil
}

type s3Object struct {
	Key          string `xml:"Key" json:"key"`
	Size         int64  `xml:"Size" json:"size"`
	LastModified string `xml:"LastModified" json:"lastModified"`
}

type s3ListResult struct {
	Contents    []s3Object `xml:"Contents"`
	IsTruncated bool       `xml:"IsTruncated"`
}

func s3ListObjects(ctx context.Context, cfg map[string]string, bucket, prefix string, max int) (*s3ListResult, error) {
	q := map[string]string{"list-type": "2"}
	if prefix != "" {
		q["prefix"] = prefix
	}
	if max > 0 {
		q["max-keys"] = strconv.Itoa(max)
	}
	resp, err := s3do(ctx, cfg, http.MethodGet, bucket, "", q, nil, emptyPayloadHash, 0, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r s3ListResult
	if err := xml.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("parse ListObjects: %w", err)
	}
	return &r, nil
}

func s3GetObject(ctx context.Context, cfg map[string]string, bucket, key, dest string) (int64, error) {
	resp, err := s3do(ctx, cfg, http.MethodGet, bucket, key, nil, nil, emptyPayloadHash, 0, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func s3PutObject(ctx context.Context, cfg map[string]string, bucket, key, src string, public bool) (int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%q is a directory", src)
	}
	// Hash the file first (streamed), then reopen it as the request body — avoids
	// buffering the whole object in memory while still signing the real payload.
	hash, err := hashFile(src)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	ct := mime.TypeByExtension(filepath.Ext(src))
	if ct == "" {
		ct = "application/octet-stream"
	}
	extra := map[string]string{"Content-Type": ct}
	if public {
		// SigV4 signs every x-amz-* header (s3do folds extra headers into the
		// signed set), so the canned ACL travels authenticated with the upload.
		extra["x-amz-acl"] = "public-read"
	}
	resp, err := s3do(ctx, cfg, http.MethodPut, bucket, key, nil, f, hash, info.Size(), extra)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return info.Size(), nil
}

// s3SetObjectACL flips the canned ACL of an existing object via the ?acl
// subresource (empty-body PUT). The bucket must allow object ACLs (e.g. AWS
// "Block Public Access" off / Object Ownership not set to BucketOwnerEnforced).
func s3SetObjectACL(ctx context.Context, cfg map[string]string, bucket, key, acl string) error {
	resp, err := s3do(ctx, cfg, http.MethodPut, bucket, key,
		map[string]string{"acl": ""}, nil, emptyPayloadHash, 0,
		map[string]string{"x-amz-acl": acl})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// s3PublicURL builds the direct, non-presigned URL of an object
// (scheme://host/[bucket/]key per the configured addressing style). It performs
// no request and does not change the ACL — the object must already be readable
// anonymously for the URL to resolve.
func s3PublicURL(cfg map[string]string, bucket, key string) (string, error) {
	if err := missing(cfg, "endpoint"); err != nil {
		return "", err
	}
	if bucket == "" || key == "" {
		return "", fmt.Errorf("bucket and key are required")
	}
	host, canonicalURI, scheme, err := s3target(cfg, bucket, key)
	if err != nil {
		return "", err
	}
	return scheme + "://" + host + canonicalURI, nil
}

func s3DeleteObject(ctx context.Context, cfg map[string]string, bucket, key string) error {
	resp, err := s3do(ctx, cfg, http.MethodDelete, bucket, key, nil, nil, emptyPayloadHash, 0, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// s3Presign builds a query-string-authenticated (presigned) URL valid for expires
// seconds. The payload is unsigned and only the host header is signed, so the URL
// is usable by any HTTP client.
func s3Presign(cfg map[string]string, method, bucket, key string, expires int) (string, error) {
	if err := missing(cfg, "endpoint", "access_key", "secret_key"); err != nil {
		return "", err
	}
	host, canonicalURI, scheme, err := s3target(cfg, bucket, key)
	if err != nil {
		return "", err
	}
	region := s3region(cfg)
	now := time.Now().UTC()
	amzDate := now.Format(amzDateFmt)
	dateStamp := now.Format(dateStampFmt)
	scope := dateStamp + "/" + region + "/" + s3Service + "/aws4_request"
	q := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    cfg["access_key"] + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(expires),
		"X-Amz-SignedHeaders": "host",
	}
	canonQuery := s3CanonicalQuery(q)
	_, sig := s3signature(cfg["secret_key"], region, dateStamp, amzDate, method,
		canonicalURI, canonQuery, []string{"host"}, map[string]string{"host": host}, unsignedPayload)
	return scheme + "://" + host + canonicalURI + "?" + canonQuery + "&X-Amz-Signature=" + sig, nil
}

// --- request plumbing + SigV4 ---

func s3region(cfg map[string]string) string {
	if r := cfg["region"]; r != "" {
		return r
	}
	return "us-east-1"
}

func s3client(cfg map[string]string) *http.Client {
	// Object transfers can be larger/slower than the framework's 15s default; the
	// per-call context still bounds the request.
	c := &http.Client{Timeout: 5 * time.Minute}
	if cfg["skip_verify"] == "true" {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return c
}

// s3target resolves the request host and canonical (URI-encoded) path for a bucket
// and key, honouring path-style (default) vs virtual-hosted addressing. A bucket of
// "" targets the service root (ListBuckets).
func s3target(cfg map[string]string, bucket, key string) (host, canonicalURI, scheme string, err error) {
	ep, err := url.Parse(strings.TrimRight(cfg["endpoint"], "/"))
	if err != nil || ep.Host == "" {
		return "", "", "", fmt.Errorf("invalid endpoint %q", cfg["endpoint"])
	}
	host, scheme = ep.Host, ep.Scheme
	escKey := s3URIEncode(key, false)
	switch {
	case bucket == "":
		canonicalURI = "/"
	case cfg["virtual_host"] == "true":
		host = bucket + "." + ep.Host
		canonicalURI = "/" + escKey
	default:
		canonicalURI = "/" + s3URIEncode(bucket, false)
		if key != "" {
			canonicalURI += "/" + escKey
		}
	}
	return host, canonicalURI, scheme, nil
}

// s3do builds, SigV4-signs and sends one S3 request, returning the response on a 2xx
// (the caller closes Body) or a parsed error otherwise.
func s3do(ctx context.Context, cfg map[string]string, method, bucket, key string,
	query map[string]string, body io.Reader, payloadHash string, contentLength int64,
	extra map[string]string) (*http.Response, error) {

	if err := missing(cfg, "endpoint", "access_key", "secret_key"); err != nil {
		return nil, err
	}
	host, canonicalURI, scheme, err := s3target(cfg, bucket, key)
	if err != nil {
		return nil, err
	}
	canonQuery := s3CanonicalQuery(query)

	reqURL := scheme + "://" + host + canonicalURI
	if canonQuery != "" {
		reqURL += "?" + canonQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Host = host
	req.ContentLength = contentLength

	now := time.Now().UTC()
	amzDate := now.Format(amzDateFmt)
	dateStamp := now.Format(dateStampFmt)
	if payloadHash == "" {
		payloadHash = emptyPayloadHash
	}

	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	values := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	for k, v := range extra {
		req.Header.Set(k, v)
		lk := strings.ToLower(k)
		signed = append(signed, lk)
		values[lk] = v
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders, signature := s3signature(cfg["secret_key"], s3region(cfg), dateStamp, amzDate,
		method, canonicalURI, canonQuery, signed, values, payloadHash)
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s/%s/%s/aws4_request, SignedHeaders=%s, Signature=%s",
		cfg["access_key"], dateStamp, s3region(cfg), s3Service, signedHeaders, signature))

	resp, err := s3client(cfg).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, s3RespError(resp)
	}
	return resp, nil
}

// s3signature computes the SigV4 signature for a canonical request and returns the
// signed-headers list alongside it. It is the pure core of the signing process,
// isolated so it can be checked against the AWS published test vectors.
func s3signature(secret, region, dateStamp, amzDate, method, canonicalURI, canonQuery string,
	signedHeaders []string, headerValues map[string]string, payloadHash string) (signedList, signature string) {

	sort.Strings(signedHeaders)
	var ch strings.Builder
	for _, h := range signedHeaders {
		ch.WriteString(h + ":" + strings.TrimSpace(headerValues[h]) + "\n")
	}
	signedList = strings.Join(signedHeaders, ";")

	canonicalRequest := strings.Join([]string{
		method, canonicalURI, canonQuery, ch.String(), signedList, payloadHash,
	}, "\n")
	scope := dateStamp + "/" + region + "/" + s3Service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonicalRequest)),
	}, "\n")

	key := s3SigningKey(secret, dateStamp, region)
	return signedList, hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
}

func s3SigningKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(s3Service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// s3CanonicalQuery builds the SigV4 canonical query string: keys and values
// URI-encoded individually, sorted by encoded key. Returns "" when empty.
func s3CanonicalQuery(q map[string]string) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, s3URIEncode(k, true)+"="+s3URIEncode(q[k], true))
	}
	return strings.Join(parts, "&")
}

// s3URIEncode encodes every byte per RFC 3986 with the unreserved set
// (A-Za-z0-9-_.~) left intact, as required by AWS SigV4. When encodeSlash is false
// the '/' is preserved (used for object-key paths); when true it is escaped to %2F
// (used for query components). AWS specifically warns against the standard library
// URL encoders here, hence this custom byte-wise implementation.
func s3URIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// s3RespError parses an S3 XML error body into a readable message.
func s3RespError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(body, &e) == nil && e.Code != "" {
		return fmt.Errorf("s3: %s (%s)", e.Message, e.Code)
	}
	return fmt.Errorf("s3: %s", resp.Status)
}

// humanBytes formats a byte count compactly (e.g. 1.5 MB).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
