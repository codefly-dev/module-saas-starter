package business_test

import (
	"context"
	"net"
	"strings"
	"testing"

	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"accounts/pkg/business"
)

// composedStorage answers Capabilities as a composed object-storage service
// does: the released 0.0.18 knows no binding and no batched read, so it
// reports neither; a release with #108's binding reports its batch size.
type composedStorage struct {
	storagev0.UnimplementedObjectStorageServer
	binding bool
}

func (c composedStorage) Capabilities(context.Context, *storagev0.CapabilitiesRequest) (*storagev0.BackendCapabilities, error) {
	if !c.binding {
		return &storagev0.BackendCapabilities{Backend: "minio"}, nil
	}
	return &storagev0.BackendCapabilities{Backend: "minio", MaxObjectBytes: 5 << 20, MaxListLimit: 1000, MaxGetManyItems: 1000}, nil
}

func composedStorageClient(t *testing.T, binding bool) storagev0.ObjectStorageClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	storagev0.RegisterObjectStorageServer(srv, composedStorage{binding: binding})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///composed",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return storagev0.NewObjectStorageClient(cc)
}

// TestObjectStorageIsRefusedAtAdmissionWhenTheComposedServiceCannotServeIt is
// review r1's major 3, pinned where the defect was: with the released 0.0.18
// composed — a service that answers, but cannot serve a binding — the catalog
// accepted new object-storage sources and AddSource stored them, and only each
// sync then failed. Wiring that service is refused and registers nothing, so
// the refusal is at admission: the catalog does not list the provider,
// AddSource refuses by name, and nothing is stored or audited. A service that
// serves the binding registers it.
func TestObjectStorageIsRefusedAtAdmissionWhenTheComposedServiceCannotServeIt(t *testing.T) {
	ctx := context.Background()
	accepts := func(svc *business.Service) bool {
		for _, e := range svc.DatasourceCatalog() {
			if e.Descriptor.Key == business.DatasourceProviderUpload {
				return e.AcceptsNewSources
			}
		}
		return false
	}
	input := business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides", Credential: "secretkey", Upload: uploadConfig(),
	}

	store := newDatasourceFakeStore()
	svc, audit := newDatasourceService(store, &recordingProducer{}, nil)
	if err := svc.SetObjectStorage(ctx, composedStorageClient(t, false)); err == nil {
		t.Fatal("wiring a composed service that cannot serve a binding succeeded")
	}
	if accepts(svc) {
		t.Fatal("the catalog accepts object-storage sources with a composed service that cannot serve them")
	}
	_, err := svc.AddSource(ctx, "actor-1", input)
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), business.ErrObjectStorageUnavailable.Error()) {
		t.Fatalf("AddSource with a service that cannot serve a binding = %v, want the unavailable refusal", err)
	}
	if sources, err := svc.ListDatasourceSources(ctx, testOrg); err != nil || len(sources) != 0 {
		t.Fatalf("a refused connect stored %v (%v)", sources, err)
	}
	if got := audit.types(); len(got) != 0 {
		t.Fatalf("a refused connect recorded %v", got)
	}

	if err := svc.SetObjectStorage(ctx, composedStorageClient(t, true)); err != nil {
		t.Fatalf("wiring a service that serves the binding = %v", err)
	}
	if !accepts(svc) {
		t.Fatal("the catalog refuses object-storage sources with a service that serves them")
	}
	if _, err := svc.AddSource(ctx, "actor-1", input); err != nil {
		t.Fatalf("AddSource with a service that serves the binding = %v", err)
	}
}
