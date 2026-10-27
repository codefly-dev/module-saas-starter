package business_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/objectstore"
)

// TestObjectStorageAsRegisteredPassesTheConformanceSuite runs the suite against
// the provider the host's registry holds once object storage is resolved —
// not against the objectstore package (whose own run is over the service's
// wire): the registry's validation, the budget meter, the per-call credential
// the host decrypts from the stored source, and the listings retained in the
// host's store, on sources connected through AddSource.
func TestObjectStorageAsRegisteredPassesTheConformanceSuite(t *testing.T) {
	connectortest.RunFiles(t, newRegisteredObjectStorage)
}

const registeredStorageSecret = "sentinel-secret-key-77e2"

type registeredObjectStorage struct {
	svc   *business.Service
	store *datasourceFakeStore
	conn  connector.FilesConnector

	mu        sync.Mutex
	objects   map[string]string
	calls     atomic.Int64
	throttled atomic.Bool
	presented atomic.Bool
}

func newRegisteredObjectStorage(t *testing.T) connectortest.FilesFixture {
	f := &registeredObjectStorage{store: newDatasourceFakeStore(), objects: map[string]string{}}
	f.svc, _ = newDatasourceService(f.store, &recordingProducer{}, nil)
	if _, ok := f.svc.DatasourceConnectors().Files(business.DatasourceProviderUpload); ok {
		t.Fatal("object storage is registered before its service resolved")
	}
	f.svc.SetDatasourceUploadBucketFactory(func(_ objectstore.SourceConfig, _, secret string) objectstore.Bucket {
		return registeredBucket{f: f, secret: secret}
	})
	conn, ok := f.svc.DatasourceConnectors().Files(business.DatasourceProviderUpload)
	if !ok {
		t.Fatal("object storage is not registered once its service resolved")
	}
	f.conn = conn
	return f
}

func (f *registeredObjectStorage) Connector() connector.FilesConnector { return f.conn }

func (f *registeredObjectStorage) Source(t *testing.T, org string) connector.Source {
	source, err := f.svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: org, Provider: business.DatasourceProviderUpload, CollectionLabel: fmt.Sprintf("guides-%d", f.calls.Load()),
		Credential: registeredStorageSecret,
		// The suite lists 120 objects: the source takes the whole bucket at
		// the object cap's ceiling.
		Upload: &business.UploadDatasourceConfig{Endpoint: "https://store.example.com", Region: "us-east-1", Bucket: "acme-docs", AccessKeyID: "AKIAEXAMPLE", MaxObjects: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	forgetConnectSync(f.svc, source.ID)
	return business.ConnectorSource(source)
}

func (f *registeredObjectStorage) Apply(t *testing.T, puts map[string]string, deletes []string, moves map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, content := range puts {
		f.objects[key] = content
	}
	for _, key := range deletes {
		delete(f.objects, key)
	}
	for from, to := range moves {
		f.objects[to] = f.objects[from]
		delete(f.objects, from)
	}
}

// Rewrite: a bucket has no history; what makes a version undiffable is that
// the host no longer retains its listing.
func (f *registeredObjectStorage) Rewrite(t *testing.T) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	clear(f.store.manifests)
}

func (f *registeredObjectStorage) ProviderCalls() int64 { return f.calls.Load() }

func (f *registeredObjectStorage) RateLimit(t *testing.T) func() {
	f.throttled.Store(true)
	return func() { f.throttled.Store(false) }
}

func (f *registeredObjectStorage) Credential() string        { return registeredStorageSecret }
func (f *registeredObjectStorage) CredentialPresented() bool { return f.presented.Load() }

func (f *registeredObjectStorage) MaxItemBytes() int64 {
	return f.conn.Descriptor().Budget.MaxItemBytes
}

// registeredBucket is the store under one call's binding: it serves only the
// secret the host decrypted for that call.
type registeredBucket struct {
	f      *registeredObjectStorage
	secret string
}

func (b registeredBucket) call(ctx context.Context) error {
	b.f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.secret != registeredStorageSecret {
		return connector.ErrCredentialRefused
	}
	b.f.presented.Store(true)
	if b.f.throttled.Load() {
		return &connector.RateLimitedError{ResetAt: time.Now().Add(time.Minute), Scope: "credential", Estimated: true}
	}
	return nil
}

func registeredETag(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

func (b registeredBucket) List(ctx context.Context) ([]objectstore.Item, error) {
	if err := b.call(ctx); err != nil {
		return nil, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	items := make([]objectstore.Item, 0, len(b.f.objects))
	for key, content := range b.f.objects {
		items = append(items, objectstore.Item{Key: key, ETag: registeredETag(content), Size: int64(len(content))})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (b registeredBucket) Read(ctx context.Context, items []objectstore.Item, visit func(objectstore.Item, string, io.Reader) error) error {
	if err := b.call(ctx); err != nil {
		return err
	}
	for _, it := range items {
		b.f.mu.Lock()
		content, ok := b.f.objects[it.Key]
		b.f.mu.Unlock()
		if !ok || registeredETag(content) != it.ETag {
			return objectstore.ErrObjectChanged
		}
		if err := visit(it, "text/markdown", strings.NewReader(content)); err != nil {
			return err
		}
	}
	return nil
}
