package objectstore

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"

	"accounts/pkg/datasource/connector"
)

// The host's deletion authority rests on two things the object-storage service
// does, not the host: a bound listing's prefix is used literally, and a listing
// the store marks incomplete fails rather than answers as a complete, possibly
// empty, one. Both once lived only in the service's implementation, and both
// were wrong there. These tests hold the service the host actually pins to
// them: they build cmd/service-object-storage from the module go.mod requires,
// run it against a scripted S3 endpoint admitted as a composed dependency, and
// read through ServiceBucket. A service version that regressed either fails
// here, before a sync could tombstone a collection on it.

// scriptedS3 answers ListObjectsV2 for one bucket, path-style, recording the
// prefix each request asked for.
type scriptedS3 struct {
	mu        sync.Mutex
	keys      map[string]string // key -> content
	truncated bool              // answer IsTruncated with no continuation token
	prefixes  []string
}

type s3Contents struct {
	Key          string `xml:"Key"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
	LastModified string `xml:"LastModified"`
}

type s3ListResult struct {
	XMLName     xml.Name     `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name        string       `xml:"Name"`
	Prefix      string       `xml:"Prefix"`
	KeyCount    int          `xml:"KeyCount"`
	MaxKeys     int          `xml:"MaxKeys"`
	IsTruncated bool         `xml:"IsTruncated"`
	Contents    []s3Contents `xml:"Contents"`
}

func (s *scriptedS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Query().Get("list-type") != "2" {
		http.Error(w, "unscripted request "+r.Method+" "+r.URL.String(), http.StatusNotImplemented)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	s.mu.Lock()
	s.prefixes = append(s.prefixes, prefix)
	res := s3ListResult{Name: "contract", Prefix: prefix, MaxKeys: 1000, IsTruncated: s.truncated}
	var keys []string
	for k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		res.Contents = append(res.Contents, s3Contents{Key: k, ETag: etagOf(s.keys[k]), Size: len(s.keys[k]), LastModified: "2026-10-23T00:00:00.000Z"})
	}
	s.mu.Unlock()
	res.KeyCount = len(res.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(res)
}

func (s *scriptedS3) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prefixes...)
}

var (
	gatewayOnce sync.Once
	gatewayBin  string
	gatewayErr  error
)

// pinnedGateway builds the gateway binary of the service-object-storage module
// this go.mod pins, once per test binary.
func pinnedGateway(t *testing.T) string {
	t.Helper()
	gatewayOnce.Do(func() {
		out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/codefly-dev/service-object-storage").Output()
		if err != nil {
			gatewayErr = fmt.Errorf("locate the pinned service-object-storage module: %w", err)
			return
		}
		dir := strings.TrimSpace(string(out))
		bin := filepath.Join(os.TempDir(), fmt.Sprintf("objectstore-contract-gateway-%d", os.Getpid()))
		build := exec.Command("go", "build", "-o", bin, "./cmd/service-object-storage")
		build.Dir = dir
		build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			gatewayErr = fmt.Errorf("build the pinned gateway in %s: %w\n%s", dir, err, out)
			return
		}
		gatewayBin = bin
	})
	if gatewayErr != nil {
		// Never a skip: a contract test that cannot run must not read as one
		// that passed.
		t.Fatal(gatewayErr)
	}
	return gatewayBin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startGateway runs the pinned gateway on its own in-memory bucket, admitting
// store — and nothing else non-public — as a composed dependency, and returns a
// client presenting its token.
func startGateway(t *testing.T, store *httptest.Server) storagev0.ObjectStorageClient {
	t.Helper()
	bin := pinnedGateway(t)
	_, storePort, _ := net.SplitHostPort(strings.TrimPrefix(store.URL, "http://"))
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"SOS_LISTEN="+listen, "SOS_BACKEND=mem", "SOS_BUCKET=own", "SOS_AUTH_TOKEN=contract-token",
		"SOS_COMPOSED_ENDPOINTS=localhost:"+storePort,
	)
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("gateway output:\n%s", logs.String())
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pinned gateway never listened on %s:\n%s", listen, logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	cc, err := Dial(listen, "contract-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return storagev0.NewObjectStorageClient(cc)
}

func contractBucket(client storagev0.ObjectStorageClient, store *httptest.Server, prefix string) *ServiceBucket {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(store.URL, "http://"))
	cfg := SourceConfig{Endpoint: "http://localhost:" + port, Region: "us-east-1", Bucket: "contract", Prefix: prefix}
	return NewServiceBucket(client, cfg, "contract-key", "contract-secret")
}

// TestPinnedServiceKeepsALiteralPrefix: the pinned service asks the store for
// exactly the source's prefix — a leading slash, a bare slash and a doubled
// separator are not normalized — and the host takes exactly the keys under it.
// A service that rewrote "/reports/" to "reports/" would list the wrong key
// space and let the closing listing tombstone the right one.
func TestPinnedServiceKeepsALiteralPrefix(t *testing.T) {
	s3 := &scriptedS3{keys: map[string]string{
		"/reports/r.md": "slashed", "reports/r.md": "plain", "/x.md": "root", "a//b/c.md": "double", "a/b/c.md": "single",
	}}
	store := httptest.NewServer(s3)
	defer store.Close()
	client := startGateway(t, store)
	for prefix, want := range map[string][]string{
		"/reports/": {"/reports/r.md"},
		"/":         {"/reports/r.md", "/x.md"},
		"a//b/":     {"a//b/c.md"},
	} {
		items, err := contractBucket(client, store, prefix).List(context.Background())
		if err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
		var got []string
		for _, it := range items {
			got = append(got, it.Key)
		}
		sort.Strings(got)
		asked := s3.asked()
		if strings.Join(got, ",") != strings.Join(want, ",") || len(asked) == 0 || asked[len(asked)-1] != prefix {
			t.Fatalf("prefix %q: listed %v after asking the store for %q, want %v listed under the literal prefix", prefix, got, asked, want)
		}
	}
}

// TestPinnedServiceRefusesAnIncompleteListing: a store that marks its listing
// truncated without a continuation token has not listed the prefix whole. The
// pinned service must fail the call; answered as a complete — here empty —
// listing, the host's closing listing would tombstone every entry.
func TestPinnedServiceRefusesAnIncompleteListing(t *testing.T) {
	for name, keys := range map[string]map[string]string{
		"empty":    {},
		"nonempty": {"kb/a.md": "alpha"},
	} {
		store := httptest.NewServer(&scriptedS3{keys: keys, truncated: true})
		client := startGateway(t, store)
		items, err := contractBucket(client, store, "kb/").List(context.Background())
		store.Close()
		if err == nil {
			t.Fatalf("%s truncated listing with no continuation token = %d items and no error, want a refusal", name, len(items))
		}
		if errors.Is(err, ErrBindingNotHonoured) {
			t.Fatalf("%s: the pinned gateway does not honour the binding: %v", name, err)
		}
	}
}

// TestPinnedServiceNamesATooLongListing: a store that answers a page with more
// entries than it was asked for has not answered the listing the host asked
// for. The pinned service refuses it and names the reason, and the host must
// read that reason as an incomplete listing — which fails the sync with
// nothing delivered — never as a throttle, which would block the credential
// for every source sharing it and retry the same listing forever.
func TestPinnedServiceNamesATooLongListing(t *testing.T) {
	keys := map[string]string{}
	for i := 0; i <= 1000; i++ {
		keys[fmt.Sprintf("kb/%04d.md", i)] = "x"
	}
	store := httptest.NewServer(&scriptedS3{keys: keys})
	defer store.Close()
	_, err := contractBucket(startGateway(t, store), store, "kb/").List(context.Background())
	var limited *connector.RateLimitedError
	if !errors.Is(err, ErrListingIncomplete) || errors.As(err, &limited) {
		t.Fatalf("a page of 1,001 entries = %v, want ErrListingIncomplete and no rate limit", err)
	}
}
