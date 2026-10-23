package business_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/objectstore"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// suiteCall is how a connector's own tests run the conformance suite.
var suiteCall = regexp.MustCompile(`connectortest\.Run[A-Z][A-Za-z]*\(t,`)

// TestEveryConformantConnectorRunsTheSuite is the conformance gate, the same
// shape as the boundary gate: a connector the host registers as conformant must
// have a conformance test beside it, in the package that implements it, that
// runs the shared suite. Registering one as conformant without that test is a
// red build. The package is the implementation's, found through the meter that
// wraps it, not the provider key's: object storage is keyed "upload" on every
// stored source and implemented in pkg/datasource/objectstore.
func TestEveryConformantConnectorRunsTheSuite(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	conformant := svc.DatasourceConnectors().Conformant()
	if len(conformant) < 2 {
		t.Fatalf("the host registers %d conformant connectors, want GitHub and object storage", len(conformant))
	}
	for _, c := range conformant {
		key := c.Descriptor().Key
		impl := any(c)
		for {
			wrapped, ok := impl.(interface {
				Unwrap() connector.FilesConnector
			})
			if !ok {
				break
			}
			impl = wrapped.Unwrap()
		}
		pkg := reflect.TypeOf(impl).Elem().PkgPath()
		rel, ok := strings.CutPrefix(pkg, "accounts/pkg/")
		if !ok {
			t.Fatalf("connector %q is implemented outside accounts/pkg (%s)", key, pkg)
		}
		dir := filepath.Join("..", rel)
		matches, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, file := range matches {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if suiteCall.Match(body) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("connector %q is registered conformant but no test in %s runs the conformance suite", key, dir)
		}
	}
}

// TestLegacyProvidersAreRegisteredWithTheirGap holds the registry to the owner's
// settled rule for providers still off the envelope: registered, flagged
// non-conformant with a named gap, not offered, not admitted — and the two on
// it, GitHub and object storage, conformant, source-scoped and admitted.
func TestLegacyProvidersAreRegisteredWithTheirGap(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	reg := svc.DatasourceConnectors()
	for _, key := range []string{business.DatasourceProviderAPI, business.DatasourceProviderCrawler} {
		d, ok := reg.Descriptor(key)
		if !ok || d.Conformant || d.Gap == "" {
			t.Fatalf("%s: descriptor %+v, want registered non-conformant with a gap", key, d)
		}
		if err := reg.AdmitNewSource(key); err != connector.ErrNonConformant {
			t.Fatalf("%s: admission = %v, want ErrNonConformant", key, err)
		}
	}
	// Object storage is admitted only where a compatible object-storage
	// service is composed (TestObjectStorageIsAdmittedOnlyWhereAServiceCanReadIt).
	svc.SetDatasourceUploadBucketFactory(func(objectstore.SourceConfig, string, string) objectstore.Bucket { return nil })
	for _, e := range svc.DatasourceCatalog() {
		if e.AcceptsNewSources != (e.Descriptor.Conformant && e.Descriptor.Readers != connector.ReadersTranslated) {
			t.Fatalf("the catalog says %s accepts new sources = %v", e.Descriptor.Key, e.AcceptsNewSources)
		}
	}
	for _, key := range []string{business.DatasourceProviderGitHub, business.DatasourceProviderUpload} {
		if d, ok := reg.Descriptor(key); !ok || !d.Conformant || d.Readers != connector.ReadersSourceScoped {
			t.Fatalf("%s: %+v, want conformant and source-scoped", key, d)
		}
		if err := reg.AdmitNewSource(key); err != nil {
			t.Fatalf("%s: admission = %v, want admitted", key, err)
		}
	}
}

// TestObjectStorageIsAdmittedOnlyWhereAServiceCanReadIt: object storage is
// conformant, but a host with no composed object-storage service — or one
// whose service predates the per-call binding, which work.go leaves unwired —
// cannot sync a bucket. The catalog says so, and AddSource refuses by name
// before anything is sealed or stored.
func TestObjectStorageIsAdmittedOnlyWhereAServiceCanReadIt(t *testing.T) {
	svc, _ := business.NewService(nil)
	svc.SetDatasourceConnector(nil, nil, "")
	accepts := func() bool {
		for _, e := range svc.DatasourceCatalog() {
			if e.Descriptor.Key == business.DatasourceProviderUpload {
				return e.AcceptsNewSources
			}
		}
		t.Fatal("object storage is not in the catalog")
		return false
	}
	if accepts() {
		t.Fatal("the catalog accepts object-storage sources with no object-storage service composed")
	}
	_, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: "11111111-1111-1111-1111-111111111111", Provider: business.DatasourceProviderUpload, CollectionLabel: "guides", Credential: "secretkey",
		Upload: &business.UploadDatasourceConfig{Endpoint: "https://store.example.com", Region: "us-east-1", Bucket: "docs", AccessKeyID: "AKIAEXAMPLE"},
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), business.ErrObjectStorageUnavailable.Error()) {
		t.Fatalf("AddSource(upload) with no object storage = %v, want the unavailable refusal", err)
	}
	svc.SetDatasourceUploadBucketFactory(func(objectstore.SourceConfig, string, string) objectstore.Bucket { return nil })
	if !accepts() {
		t.Fatal("the catalog refuses object-storage sources with an object-storage service composed")
	}
}
