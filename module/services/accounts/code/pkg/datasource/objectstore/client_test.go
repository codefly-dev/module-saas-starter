package objectstore

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// allowDial overrides the SSRF dial guard so tests can reach httptest's
// 127.0.0.1 listener, restoring the real guard when the test finishes.
func allowDial(t *testing.T) {
	t.Helper()
	saved := dialGuard
	dialGuard = func(_, _ string, _ syscall.RawConn) error { return nil }
	t.Cleanup(func() { dialGuard = saved })
}

func TestFetchHappyPath(t *testing.T) {
	allowDial(t)

	const bucket = "my-bucket"
	const prefix = "docs/"
	var sawAuth, sawDate bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		if r.Header.Get("x-amz-date") != "" {
			sawDate = true
		}
		if a := r.Header.Get("Authorization"); a != "" && !strings.HasPrefix(a, "AWS4-HMAC-SHA256") {
			t.Errorf("Authorization not AWS4-HMAC-SHA256: %q", a)
		}
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Name>my-bucket</Name>
  <Prefix>docs/</Prefix>
  <Contents><Key>docs/a.txt</Key><ETag>"etag-a"</ETag></Contents>
  <Contents><Key>docs/b.txt</Key><ETag>"etag-b"</ETag></Contents>
</ListBucketResult>`))
			return
		}
		switch r.URL.Path {
		case "/my-bucket/docs/a.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("ETag", `"etag-a"`)
			w.Write([]byte("alpha"))
		case "/my-bucket/docs/b.txt":
			w.Header().Set("Content-Type", "text/markdown")
			w.Header().Set("ETag", `"etag-b"`)
			w.Write([]byte("bravo"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(Config{
		Endpoint:    srv.URL,
		Region:      "us-east-1",
		Bucket:      bucket,
		Prefix:      prefix,
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
	}, "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY")

	objs, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 objects, got %d", len(objs))
	}
	byKey := map[string]Object{}
	for _, o := range objs {
		byKey[o.Key] = o
	}
	a, ok := byKey["docs/a.txt"]
	if !ok {
		t.Fatalf("missing docs/a.txt in %v", byKey)
	}
	if string(a.Body) != "alpha" || a.ContentType != "text/plain" || a.ETag != `"etag-a"` {
		t.Errorf("a mismatch: body=%q ct=%q etag=%q", a.Body, a.ContentType, a.ETag)
	}
	b, ok := byKey["docs/b.txt"]
	if !ok {
		t.Fatalf("missing docs/b.txt in %v", byKey)
	}
	if string(b.Body) != "bravo" || b.ContentType != "text/markdown" || b.ETag != `"etag-b"` {
		t.Errorf("b mismatch: body=%q ct=%q etag=%q", b.Body, b.ContentType, b.ETag)
	}
	if !sawAuth {
		t.Error("server never saw an Authorization header")
	}
	if !sawDate {
		t.Error("server never saw an x-amz-date header")
	}
}

// TestAuthorizationHeaderStructure asserts the shape of the Authorization header
// a real signed request carries.
func TestAuthorizationHeaderStructure(t *testing.T) {
	allowDial(t)

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<ListBucketResult></ListBucketResult>`))
	}))
	defer srv.Close()

	c := New(Config{
		Endpoint:    srv.URL,
		Region:      "us-east-1",
		Bucket:      "b",
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
	}, "secret")
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	re := regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=AKIA[^ ]+/\d{8}/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=[0-9a-f]{64}$`)
	if !re.MatchString(auth) {
		t.Fatalf("Authorization header does not match expected structure: %q", auth)
	}
}

// TestSigV4KnownAnswer verifies the signing-key derivation and final signature
// against AWS's published S3 "GET Object" SigV4 example (service s3, region
// us-east-1, secret wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY).
// https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html
//
// The canonical request below is the documented one; its SHA-256 is AWS's
// published intermediate hash 7344ae5b...946972, and the resulting signature is
// the value AWS's own worked example computes.
func TestSigV4KnownAnswer(t *testing.T) {
	canonicalRequest := strings.Join([]string{
		"GET",
		"/test.txt",
		"",
		"host:examplebucket.s3.amazonaws.com",
		"range:bytes=0-9",
		"x-amz-content-sha256:" + emptyPayloadHash,
		"x-amz-date:20130524T000000Z",
		"",
		"host;range;x-amz-content-sha256;x-amz-date",
		emptyPayloadHash,
	}, "\n")

	crHash := sha256Hex([]byte(canonicalRequest))
	const wantCRHash = "7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972"
	if crHash != wantCRHash {
		t.Fatalf("canonical-request hash mismatch:\n got %s\nwant %s", crHash, wantCRHash)
	}

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		"20130524T000000Z",
		"20130524/us-east-1/s3/aws4_request",
		crHash,
	}, "\n")

	key := signingKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20130524", "us-east-1", "s3")
	got := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	const want = "67fe34c8530db585abddc51067328adfedb6e42487d2566dc7d927d6e2722900"
	if got != want {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestSigV4TestSuiteGetVanilla anchors the signing routine to the independent
// AWS SigV4 test-suite "get-vanilla" vector (service "service"), whose published
// signature is a fixed known answer — proving signingKey/hmac/sha256 are correct
// independently of this connector's own request-building.
func TestSigV4TestSuiteGetVanilla(t *testing.T) {
	canonicalRequest := strings.Join([]string{
		"GET",
		"/",
		"",
		"host:example.amazonaws.com",
		"x-amz-date:20150830T123600Z",
		"",
		"host;x-amz-date",
		emptyPayloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		"20150830T123600Z",
		"20150830/us-east-1/service/aws4_request",
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	key := signingKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20150830", "us-east-1", "service")
	got := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	const want = "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got != want {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestEmptyPayloadHash guards the well-known empty-string SHA-256 constant used
// for x-amz-content-sha256 on the bodyless GET requests.
func TestEmptyPayloadHash(t *testing.T) {
	if got := sha256Hex(nil); got != emptyPayloadHash {
		t.Fatalf("empty payload hash mismatch: got %s want %s", got, emptyPayloadHash)
	}
}

func TestFetchSkipsFailingObject(t *testing.T) {
	allowDial(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(`<ListBucketResult>
  <Contents><Key>ok.txt</Key><ETag>"e1"</ETag></Contents>
  <Contents><Key>boom.txt</Key><ETag>"e2"</ETag></Contents>
</ListBucketResult>`))
			return
		}
		switch r.URL.Path {
		case "/b/ok.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("fine"))
		case "/b/boom.txt":
			http.Error(w, "kaboom", http.StatusInternalServerError)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(Config{
		Endpoint:    srv.URL,
		Region:      "us-east-1",
		Bucket:      "b",
		AccessKeyID: "AKIAEXAMPLE",
	}, "secret")

	objs, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("want 1 object (the 500 skipped), got %d: %+v", len(objs), objs)
	}
	if objs[0].Key != "ok.txt" || string(objs[0].Body) != "fine" {
		t.Errorf("unexpected surviving object: %+v", objs[0])
	}
}

// TestSSRFGuardBlocksLoopback confirms the dial guard is wired by default (no
// override): a loopback endpoint is refused.
func TestSSRFGuardBlocksLoopback(t *testing.T) {
	c := New(Config{
		Endpoint:    "http://127.0.0.1:1",
		Bucket:      "b",
		Region:      "us-east-1",
		AccessKeyID: "AKIAEXAMPLE",
	}, "secret")
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error fetching a loopback endpoint, got nil")
	}
}
