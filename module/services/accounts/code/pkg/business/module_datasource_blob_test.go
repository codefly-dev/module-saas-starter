package business_test

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"

	"google.golang.org/grpc/codes"
)

// moduleBlobService wires a Service with both the datasource connector (cipher +
// fake GitHub) and a module principal registry, so ModuleFetchDatasourceBlob can
// be exercised end to end without a database or a live api.github.com. The
// principal holds the datasource queue; crossTenant decides whether it may reach
// sources outside its bound org.
func moduleBlobService(t *testing.T, store business.Store, gh business.GitHubContentClient, queues []string, crossTenant bool) *business.Service {
	t.Helper()
	svc, audit := newDatasourceService(store, &recordingProducer{}, gh)
	_ = audit
	svc.SetModuleCapabilities(&fakeJobBackend{}, &fakeJobBackend{}, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: queues, CrossTenant: crossTenant},
	})
	return svc
}

// seedBlobSource inserts an active GitHub source owned by orgID whose access
// token round-trips through the fake cipher, and points the fake GitHub at a
// single blob.
func seedBlobSource(t *testing.T, svc *business.Service, orgID string, blobSHA string, content []byte, gh *fakeGitHub) *business.DatasourceSource {
	t.Helper()
	gh.blobs = map[string][]byte{blobSHA: content}
	source, err := svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID:           orgID,
		Repo:            "acme/docs",
		CollectionLabel: "wiki",
		AccessToken:     "ghp_token",
	})
	if err != nil {
		t.Fatalf("AddGitHubSource: %v", err)
	}
	return source
}

func TestModuleFetchDatasourceBlob_HappyPath(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"datasource"}, true)
	source := seedBlobSource(t, svc, testOrg, "sha-1", []byte("# Docs\nhello\n"), gh)

	content, contentType, err := svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(), source.ID, "sha-1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(content) != "# Docs\nhello\n" {
		t.Fatalf("unexpected content %q", content)
	}
	if !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("expected text content type, got %q", contentType)
	}
}

func TestModuleFetchDatasourceBlob_UnknownPrincipalRejected(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"datasource"}, true)
	source := seedBlobSource(t, svc, testOrg, "sha-1", []byte("x"), gh)

	_, _, err := svc.ModuleFetchDatasourceBlob(context.Background(),
		business.ModuleCaller{PrincipalID: "someone-else", BoundOrg: moduleTenantA}, source.ID, "sha-1")
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleFetchDatasourceBlob_DisallowedQueueRejected pins that fetching a
// blob requires the datasource queue grant specifically: a principal registered
// for another queue is denied before any source is loaded.
func TestModuleFetchDatasourceBlob_DisallowedQueueRejected(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"documents"}, true)
	source := seedBlobSource(t, svc, testOrg, "sha-1", []byte("x"), gh)

	_, _, err := svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(), source.ID, "sha-1")
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleFetchDatasourceBlob_CrossTenantSourceDeniedWithoutGrant pins the
// authorization boundary: the fetch is authorized against the source row's own
// org, so a principal bound to tenant A without a cross-tenant grant cannot read
// a source owned by tenant B even though it holds the datasource queue.
func TestModuleFetchDatasourceBlob_CrossTenantSourceDeniedWithoutGrant(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"datasource"}, false)
	source := seedBlobSource(t, svc, moduleTenantB, "sha-1", []byte("secret"), gh)

	_, _, err := svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(), source.ID, "sha-1")
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleFetchDatasourceBlob_OwnOrgSourceAllowedWithoutCrossTenant is the
// complement: an org-bound principal reaches a source owned by its own bound org.
func TestModuleFetchDatasourceBlob_OwnOrgSourceAllowedWithoutCrossTenant(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"datasource"}, false)
	source := seedBlobSource(t, svc, moduleTenantA, "sha-1", []byte("owned"), gh)

	content, _, err := svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(), source.ID, "sha-1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(content) != "owned" {
		t.Fatalf("unexpected content %q", content)
	}
}

func TestModuleFetchDatasourceBlob_MissingSourceNotFound(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"datasource"}, true)

	_, _, err := svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(),
		"00000000-0000-0000-0000-0000000000ff", "sha-1")
	requireCode(t, err, codes.NotFound)
}

// TestModuleFetchDatasourceBlob_TooLargeRefused pins that an oversized blob is
// refused as FailedPrecondition rather than buffered.
func TestModuleFetchDatasourceBlob_TooLargeRefused(t *testing.T) {
	gh := &fakeGitHub{}
	svc := moduleBlobService(t, newDatasourceFakeStore(), gh, []string{"datasource"}, true)
	source := seedBlobSource(t, svc, testOrg, "sha-1", []byte("x"), gh)
	gh.blobErrs = map[string]error{"sha-1": github.ErrFileTooLarge}

	_, _, err := svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(), source.ID, "sha-1")
	requireCode(t, err, codes.FailedPrecondition)
}

// TestModuleFetchDatasourceBlob_ConnectorUnconfigured pins the fail-closed guard
// when the datasource connector was never wired.
func TestModuleFetchDatasourceBlob_ConnectorUnconfigured(t *testing.T) {
	svc, err := business.NewService(newDatasourceFakeStore())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(&fakeJobBackend{}, &fakeJobBackend{}, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"datasource"}, CrossTenant: true},
	})

	_, _, err = svc.ModuleFetchDatasourceBlob(context.Background(), moduleCaller(), "src", "sha-1")
	requireCode(t, err, codes.FailedPrecondition)
}
