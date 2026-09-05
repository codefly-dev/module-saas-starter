// Package objectstore is the S3-compatible object-storage datasource connector:
// given an endpoint, region, bucket, prefix, and AWS-style credentials, it lists
// the objects under the prefix (S3 ListObjectsV2) and fetches each one
// (GetObject), signing every request with AWS Signature Version 4. It addresses
// objects path-style ({endpoint}/{bucket}/{key}) so it works with MinIO and
// other S3-compatible stores as well as AWS S3. It holds no persistence or
// key-management concerns of its own — the business layer decrypts the secret
// access key and hands it in plaintext per client.
//
// The endpoint is tenant-supplied, so every dial is guarded against SSRF: a URL
// that resolves to a private, loopback, link-local, or otherwise non-public
// address is refused, on the initial request and on any redirect, checked
// against the concrete IP being connected to (not the hostname) so DNS
// rebinding cannot slip past the check.
package objectstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// maxResponseBytes bounds one listing or object body so a pathological endpoint
// cannot exhaust memory or overflow the downstream job payload limit.
const maxResponseBytes = 5 * 1024 * 1024

// defaultMaxObjects caps how many objects one Fetch returns when the source does
// not configure a limit; maxObjectsCeiling is the hard upper bound regardless of
// configuration.
const (
	defaultMaxObjects = 100
	maxObjectsCeiling = 1000
)

const maxRedirects = 3

// emptyPayloadHash is the SHA-256 of the empty payload, used as
// x-amz-content-sha256 for the GET requests this connector issues (they carry no
// body).
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// ErrBlockedAddress is returned when a request target (or a redirect) resolves to
// a non-public IP address. It is deliberately opaque so a tenant cannot use the
// connector to map the cluster's internal network.
var ErrBlockedAddress = errors.New("objectstore: request target is not a public address")

// ErrObjectNotFound reports that a listed key returned 404 on GET — it was
// deleted between the listing and the fetch. The caller may skip it best-effort.
var ErrObjectNotFound = errors.New("objectstore: object not found")

// ErrObjectTooLarge reports that an object body exceeds the connector's transport
// ceiling. It is distinct from a generic failure so the caller can surface an
// undeliverable object rather than silently discard it.
var ErrObjectTooLarge = errors.New("objectstore: object exceeds the maximum size")

// Config is the non-secret configuration of one object-storage source.
type Config struct {
	Endpoint    string // absolute http(s) base URL, e.g. https://s3.us-east-1.amazonaws.com or a MinIO endpoint
	Region      string // e.g. "us-east-1"
	Bucket      string
	Prefix      string // key prefix to list under; may be empty
	AccessKeyID string
	MaxObjects  int // 0 means the default cap
}

// Object is one fetched object: its key, body bytes, content type, and ETag.
type Object struct {
	Key         string
	Body        []byte
	ContentType string
	ETag        string
}

// Client lists and fetches the objects of a single object-storage source.
type Client struct {
	cfg     Config
	secret  string
	base    *url.URL
	baseErr error
	http    *http.Client
}

// dialGuard refuses to open a connection to a non-public address. It runs after
// DNS resolution with the concrete IP:port being dialed, so it blocks the exact
// address the connection would reach — closing the DNS-rebinding gap a
// hostname-only check leaves open. It is a package-level var so tests, which must
// reach httptest's 127.0.0.1 listener, can override it.
var dialGuard = func(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return ErrBlockedAddress
	}
	return nil
}

// New returns a Client for cfg, authenticating with the plaintext secret access
// key. The HTTP client blocks connections to non-public addresses.
func New(cfg Config, secretAccessKey string) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			// Indirect through the dialGuard var so a test override is honored.
			Control: func(network, address string, c syscall.RawConn) error {
				return dialGuard(network, address, c)
			},
		}).DialContext,
	}
	c := &Client{
		cfg:    cfg,
		secret: secretAccessKey,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return errors.New("objectstore: too many redirects")
				}
				return nil
			},
		},
	}
	base, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	switch {
	case err != nil:
		c.baseErr = fmt.Errorf("objectstore: invalid endpoint: %w", err)
	case base.Scheme != "http" && base.Scheme != "https":
		c.baseErr = errors.New("objectstore: endpoint must be http or https")
	case base.Host == "":
		c.baseErr = errors.New("objectstore: endpoint must have a host")
	default:
		c.base = base
	}
	return c
}

// Entry is one row of a ListObjectsV2 result: an object key and its ETag.
type Entry struct {
	Key  string
	ETag string
}

// List issues one ListObjectsV2 request and returns its entries, bounded by the
// configured object cap. Listing failure (including a non-2xx list) is a returned
// error so a credential or permission problem surfaces rather than reading as an
// empty bucket.
func (c *Client) List(ctx context.Context) ([]Entry, error) {
	if c.baseErr != nil {
		return nil, c.baseErr
	}
	u := *c.base
	u.Path = "/" + c.cfg.Bucket
	u.RawPath = "/" + rfc3986Escape(c.cfg.Bucket)
	// Canonical query string: sorted key=value, RFC3986-encoded. "list-type"
	// sorts before "prefix", so this is already canonical order.
	u.RawQuery = "list-type=2&prefix=" + rfc3986Escape(c.cfg.Prefix)

	body, status, _, err := c.do(ctx, &u)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("objectstore: list returned status %d", status)
	}
	var parsed listBucketResult
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("objectstore: parse listing: %w", err)
	}
	limit := c.cfg.MaxObjects
	if limit <= 0 {
		limit = defaultMaxObjects
	}
	if limit > maxObjectsCeiling {
		limit = maxObjectsCeiling
	}
	entries := make([]Entry, 0, len(parsed.Contents))
	for _, e := range parsed.Contents {
		if len(entries) >= limit {
			break
		}
		entries = append(entries, Entry{Key: e.Key, ETag: e.ETag})
	}
	return entries, nil
}

// listEntry is one <Contents> row of a ListBucketResult.
type listEntry struct {
	Key  string `xml:"Key"`
	ETag string `xml:"ETag"`
}

type listBucketResult struct {
	XMLName  xml.Name    `xml:"ListBucketResult"`
	Contents []listEntry `xml:"Contents"`
}

// Fetch retrieves one object by key. The caller drives it per Entry from List, so
// only one object body is ever held in memory at a time. A 404 returns
// ErrObjectNotFound (the key was deleted between listing and fetch); a body over
// the transport ceiling returns ErrObjectTooLarge; any other non-2xx or transport
// error is returned so the caller can treat it as a real failure.
func (c *Client) Fetch(ctx context.Context, key string) (Object, error) {
	if c.baseErr != nil {
		return Object{}, c.baseErr
	}
	u := *c.base
	u.Path = "/" + c.cfg.Bucket + "/" + key
	// Path-escape each key segment but keep the "/" separators.
	u.RawPath = "/" + rfc3986Escape(c.cfg.Bucket) + "/" + encodeKeyPath(key)
	u.RawQuery = ""

	body, status, header, err := c.do(ctx, &u)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return Object{}, ErrObjectTooLarge
		}
		return Object{}, err
	}
	if status == http.StatusNotFound {
		return Object{}, ErrObjectNotFound
	}
	if status < 200 || status >= 300 {
		return Object{}, fmt.Errorf("objectstore: get %q returned status %d", key, status)
	}
	return Object{
		Key:         key,
		Body:        body,
		ContentType: header.Get("Content-Type"),
		ETag:        header.Get("ETag"),
	}, nil
}

// errBodyTooLarge is the internal signal do() raises when a response body exceeds
// the transport ceiling; Fetch translates it to the exported ErrObjectTooLarge.
var errBodyTooLarge = errors.New("objectstore: response exceeds the maximum size")

// do builds, signs, and sends a GET request for u, returning the bounded body,
// the status code, and the response header. An over-large body is errBodyTooLarge.
func (c *Client) do(ctx context.Context, u *url.URL) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String(), nil)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("objectstore: build request: %w", err)
	}
	// Use the exact escaped path/query we built, not a re-parsed copy.
	req.URL = u
	req.Host = u.Host
	c.sign(req, emptyPayloadHash, time.Now())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("objectstore: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("objectstore: read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, 0, nil, errBodyTooLarge
	}
	return body, resp.StatusCode, resp.Header, nil
}

// sign applies AWS Signature Version 4 (service "s3") to req in place. It signs
// the host, x-amz-content-sha256, and x-amz-date headers.
func (c *Client) sign(req *http.Request, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		req.URL.RawQuery, // already canonical: sorted, RFC3986-encoded
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + c.cfg.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	key := signingKey(c.secret, dateStamp, c.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.cfg.AccessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
}

// signingKey derives the SigV4 signing key: AWS4+secret, then successively keyed
// by the date stamp, region, service, and the literal "aws4_request".
func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// encodeKeyPath path-escapes each "/"-separated segment of an S3 key while
// keeping the separators, per the S3 canonical-URI rules.
func encodeKeyPath(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = rfc3986Escape(s)
	}
	return strings.Join(segments, "/")
}

// rfc3986Escape percent-encodes s per RFC 3986, leaving only the unreserved set
// (A-Z a-z 0-9 - _ . ~) unescaped — the encoding SigV4 canonicalization expects.
func rfc3986Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", ch))
	}
	return b.String()
}
