package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

const sourceA = "019f6bf7-0000-7000-8000-000000000001"
const sourceB = "019f6bf7-0000-7000-8000-000000000002"
const sourceR = "019f6bf7-0000-7000-8000-000000000003"

const readOrg = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
const readOwner = "019f6bf7-5b1c-730d-9687-fe6d4aff31ed"

var changesEnqueuedAt = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
var syncRequestedAt = time.Date(2026, 3, 4, 4, 0, 0, 0, time.UTC)

type readProjectionStore struct {
	business.Store
	grants                  map[string][]*gen.AccessibleScope
	sources                 []*business.DatasourceSource
	err                     error
	resources               []string
	nodeResource            map[string]string
	nodeLabel               map[string]string
	wildcard                bool
	scopeCalls, sourceCalls int
	revision                int
	expires                 time.Time

	collectionGrants           map[string][]*gen.ReadableCollectionGrant
	syncRequests               map[string]business.SourceSyncRequest
	grantCalls, requesterCalls int
	grantResources             []string
}

func (f *readProjectionStore) WithSourceReadSnapshot(ctx context.Context, org string, run func(context.Context) error) error {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, readOwner); err != nil {
		return err
	}
	return run(ctx)
}
func (f *readProjectionStore) SourceReadRevision(context.Context, string, []string) (string, time.Time, error) {
	return fmt.Sprint(f.revision), f.expires, f.err
}
func (f *readProjectionStore) ListReadableSourcesPage(_ context.Context, org string, subjects, resources []string, after string, limit int) ([]*gen.ReadableSourceCollection, error) {
	f.scopeCalls++
	f.sourceCalls++
	f.resources = resources
	var out []*gen.ReadableSourceCollection
	for _, source := range f.sources {
		if source.OrgID != org {
			return nil, errors.New("unexpected tenant")
		}
		allowed := len(resources) > 0 &&
			(f.wildcard || slices.Contains(resources, f.nodeResourceType(source.BoundaryNodeID)))
		for _, subject := range subjects {
			found := false
			for _, scope := range f.grants[subject] {
				if scope.NodeId == source.BoundaryNodeID {
					found = true
				}
			}
			allowed = allowed && found
		}
		if !allowed || source.ID <= after {
			continue
		}
		// Mirrors the production rule: GitHub is addressed by repository, every
		// other provider by its source id.
		container := source.ID
		if source.Provider == "github" {
			if source.Repo == "" {
				return nil, status.Error(codes.FailedPrecondition, "source attribution is incomplete")
			}
			container = source.Repo
		}
		ref := source.Branch
		if ref != "" && !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		collection := &gen.ReadableSourceCollection{SourceId: source.ID, BoundaryId: source.BoundaryNodeID, Origin: source.Provider, Container: container, Ref: ref, Paths: source.Paths, BoundaryLabel: f.nodeLabel[source.BoundaryNodeID]}
		if source.LastIngestedAt != nil {
			collection.Sync = &gen.CollectionSyncProvenance{
				Stage:    gen.SourceSyncStage_SOURCE_SYNC_STAGE_CHANGES_ENQUEUED,
				At:       timestamppb.New(*source.LastIngestedAt),
				Revision: source.LastIngestedCommit,
				Trigger:  source.LastDeliveryID,
			}
		}
		out = append(out, collection)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceId < out[j].SourceId })
	return out[:min(limit, len(out))], f.err
}

// nodeResourceType mirrors scope_nodes.resource_type: the type a collection's
// records are governed by, which the declared set is matched against.
func (f *readProjectionStore) nodeResourceType(node string) string {
	if kind, ok := f.nodeResource[node]; ok {
		return kind
	}
	return "documents"
}

func (f *readProjectionStore) ReadableCollectionGrants(_ context.Context, org string, boundaries, resources []string) (map[string][]*gen.ReadableCollectionGrant, error) {
	f.grantCalls++
	f.grantResources = resources
	if org != readOrg {
		return nil, errors.New("unexpected tenant")
	}
	out := map[string][]*gen.ReadableCollectionGrant{}
	for _, boundary := range boundaries {
		if grants, ok := f.collectionGrants[boundary]; ok {
			out[boundary] = grants
		}
	}
	return out, f.err
}

func (f *readProjectionStore) LatestSourceSyncRequests(_ context.Context, org string, sources []string) (map[string]business.SourceSyncRequest, error) {
	f.requesterCalls++
	if org != readOrg {
		return nil, errors.New("unexpected tenant")
	}
	out := map[string]business.SourceSyncRequest{}
	for _, source := range sources {
		if request, ok := f.syncRequests[source]; ok {
			out[source] = request
		}
	}
	return out, f.err
}

type sourceReadIdentityAuthority struct{ *workContextAuthorityFake }

func (a sourceReadIdentityAuthority) ResolveWorkContextAuthority(ctx context.Context, org, owner, actor string, permissions []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, owner); err != nil {
		return nil, err
	}
	return a.workContextAuthorityFake.ResolveWorkContextAuthority(ctx, org, owner, actor, permissions)
}

func sourceReadFixture(t *testing.T) (*readProjectionStore, *workContextAuthorityFake, accountsv1connect.ModuleCapabilitiesServiceClient, func(string, string, string, ...string) string) {
	t.Helper()
	previousService, previousAuthority := service, workContextSingleton
	t.Cleanup(func() { service, workContextSingleton = previousService, previousAuthority; SetInternalToken("") })
	store := &readProjectionStore{grants: map[string][]*gen.AccessibleScope{readOwner: {{NodeId: "boundary-a", ScopePath: "a"}, {NodeId: "boundary-b", ScopePath: "b"}, {NodeId: "boundary-r", ScopePath: "r"}}}, nodeResource: map[string]string{"boundary-r": "rows"},
		nodeLabel: map[string]string{"boundary-a": "Example Handbook", "boundary-b": "Example Policies", "boundary-r": "Example Ledger"},
		collectionGrants: map[string][]*gen.ReadableCollectionGrant{"boundary-a": {
			{SubjectLabel: "Example Reader", SubjectKind: "principal", RoleName: "reader", ScopePath: "a"},
			{SubjectLabel: "Example Team", SubjectKind: "team", RoleName: "reader", ScopePath: "root", Inherited: true},
		}},
		syncRequests: map[string]business.SourceSyncRequest{sourceA: {RequestedAt: syncRequestedAt, RequestedBy: "Example Operator"}},
		sources: []*business.DatasourceSource{
			{ID: sourceA, OrgID: readOrg, Provider: "github", Repo: "acme/handbook", BoundaryNodeID: "boundary-a", Branch: "main", Paths: []string{"docs/"},
				LastIngestedAt: &changesEnqueuedAt, LastIngestedCommit: "0e57a1c", LastDeliveryID: "delivery-1"},
			{ID: sourceB, OrgID: readOrg, Provider: "github", Repo: "acme/policies", BoundaryNodeID: "boundary-b", Branch: "release"},
			{ID: "source-denied", OrgID: readOrg, Provider: "github", Repo: "acme/private", BoundaryNodeID: "boundary-denied"},
			{ID: sourceR, OrgID: readOrg, Provider: "github", Repo: "acme/ledger", BoundaryNodeID: "boundary-r", Branch: "main"},
		}}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	// Two composed modules whose content is governed by unrelated resource types:
	// nothing this host authorizes may depend on which of them is calling.
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		business.ModulePrincipalID("documents"): {Prefix: "documents", Resources: []string{"documents"}},
		business.ModulePrincipalID("rows"):      {Prefix: "rows", Resources: []string{"rows"}},
	})
	facts := &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	workContextSingleton = &WorkContextAuthorityServer{}
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "test-key", PrivateKey: key, Authority: sourceReadIdentityAuthority{facts}})
	SetInternalToken("source-read-test-perimeter")
	internal := grpc.NewServer(grpc.UnaryInterceptor(grpcAuthInterceptor(nil, rpcExposureInternal)))
	gen.RegisterModuleCapabilitiesServiceServer(internal, &ModuleCapabilitiesServer{})
	t.Cleanup(internal.Stop)
	server := httptest.NewUnstartedServer(multiplexInternalGRPC(internal, http.NotFoundHandler()))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := accountsv1connect.NewModuleCapabilitiesServiceClient(server.Client(), server.URL, connect.WithGRPC())
	mint := func(audience, kind, action string, extra ...string) string {
		scopes := []*basev0.WorkScopeV1{{ResourceKind: kind, Actions: []string{action}}}
		for _, permission := range extra {
			resource, verb, _ := strings.Cut(permission, ":")
			scopes = append(scopes, &basev0.WorkScopeV1{ResourceKind: resource, Actions: []string{verb}})
		}
		token, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: audience, TenantID: readOrg, OwnerPrincipalID: readOwner,
			TaskID: "019f6bf7-1111-7111-8111-111111111111", SessionID: "019f6bf7-2222-7222-8222-222222222222", AuthorizationRevision: facts.facts.EffectiveRevision(), ReplayPolicy: codefly.WorkContextReplayIdempotent,
			AuthorityScopes: scopes})
		require.NoError(t, err)
		return token.Encoded()
	}
	return store, facts, client, mint
}
func sourceReadRequest(token string) *connect.Request[gen.ListReadableSourceCollectionsRequest] {
	r := connect.NewRequest(&gen.ListReadableSourceCollectionsRequest{PageSize: 1})
	r.Header().Set(codefly.WorkContextHeaderName, token)
	r.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	return r
}
func TestSourceReadSignedConnectProjectionAndRevocation(t *testing.T) {
	store, facts, client, mint := sourceReadFixture(t)
	ctx := context.Background()
	token := mint("documents", "documents", "read")
	first, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Len(t, first.Msg.Collections, 1)
	require.Equal(t, sourceA, first.Msg.Collections[0].SourceId)
	require.Equal(t, "refs/heads/main", first.Msg.Collections[0].Ref)
	require.Equal(t, []string{"docs/"}, first.Msg.Collections[0].Paths)
	next := sourceReadRequest(token)
	next.Msg.PageToken = first.Msg.NextPageToken
	second, err := client.ListReadableSourceCollections(ctx, next)
	require.NoError(t, err)
	require.Equal(t, sourceB, second.Msg.Collections[0].SourceId)
	require.Empty(t, second.Msg.NextPageToken)
	// A current grant deletion invalidates a page even with the original token.
	store.grants[readOwner] = nil
	store.revision++
	_, err = client.ListReadableSourceCollections(ctx, next)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	empty, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Empty(t, empty.Msg.Collections)
	// A revoked authority revision cannot be refreshed by replaying its token.
	facts.facts.OrganizationRevision++
	_, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
func TestSourceReadRejectsUntrustedAuthorityBeforeStorage(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	for _, test := range []struct {
		name, token string
		perimeter   bool
		duplicate   bool
	}{
		{name: "forged", token: "not-signed", perimeter: true}, {name: "unregistered audience", token: mint("other", "other", "read"), perimeter: true},
		{name: "undeclared resource kind", token: mint("documents", "rows", "read"), perimeter: true},
		{name: "scope attenuation", token: mint("documents", "documents", "write"), perimeter: true}, {name: "missing perimeter", token: mint("documents", "documents", "read")},
		{name: "ambiguous carrier", token: mint("documents", "documents", "read"), perimeter: true, duplicate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := sourceReadRequest(test.token)
			if !test.perimeter {
				r.Header().Del("x-codefly-internal-token")
			}
			if test.duplicate {
				r.Header().Add(codefly.WorkContextHeaderName, test.token)
			}
			r.Header().Set("x-tenant-id", readOrg)
			r.Header().Set("x-user-id", readOwner)
			_, err := client.ListReadableSourceCollections(context.Background(), r)
			require.Error(t, err)
		})
	}
	require.Zero(t, store.scopeCalls)
	require.Zero(t, store.sourceCalls)
}
func TestSourceReadCompleteScopePaginationAndPartialFailure(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	store.grants[readOwner] = nil
	for i := 0; i < 1001; i++ {
		store.grants[readOwner] = append(store.grants[readOwner], &gen.AccessibleScope{NodeId: fmt.Sprintf("boundary-%04d", i), ScopePath: fmt.Sprintf("scope%04d", i)})
	}
	store.sources[0].BoundaryNodeID = "boundary-1000"
	result, err := client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "documents", "read")))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)
	require.Equal(t, 1, store.scopeCalls)
	store.err = errors.New("source backend unavailable")
	result, err = client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "documents", "read")))
	require.Error(t, err)
	require.Nil(t, result)
}
func TestSourceReadIntersectsDelegatedSubjectsAndProjectsEveryProvider(t *testing.T) {
	store, _, _, _ := sourceReadFixture(t)
	store.grants["actor"] = []*gen.AccessibleScope{{NodeId: "boundary-b", ScopePath: "b"}}
	r, err := service.ReadableSourceCollections(auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg), readOrg, []string{readOwner, "actor"}, []string{"documents"}, business.CollectionMetadataDisclosure{}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
	require.NoError(t, err)
	require.Len(t, r.Collections, 1)
	require.Equal(t, sourceB, r.Collections[0].SourceId)
	require.Equal(t, "acme/policies", r.Collections[0].Container)
	// The same intersection projects a non-GitHub source, keyed by its source id
	// rather than a repository it never had.
	store.sources[1].Provider, store.sources[1].Repo = "upload", ""
	r, err = service.ReadableSourceCollections(auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg), readOrg, []string{readOwner, "actor"}, []string{"documents"}, business.CollectionMetadataDisclosure{}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
	require.NoError(t, err)
	require.Len(t, r.Collections, 1)
	require.Equal(t, "upload", r.Collections[0].Origin)
	require.Equal(t, sourceB, r.Collections[0].Container)
	store.sources[1].OrgID = "another-tenant"
	r, err = service.ReadableSourceCollections(auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg), readOrg, []string{readOwner}, []string{"documents"}, business.CollectionMetadataDisclosure{}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
	require.Error(t, err)
	require.Nil(t, r)
}

// The host holds no domain content, so which resource type governs a collection
// is the calling module's declaration to make — not a literal in this repository.
func TestSourceReadAuthorizesEachModuleUnderItsOwnDeclaredResources(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	ctx := context.Background()
	page := func(module string) []string {
		r := sourceReadRequest(mint(module, module, "read"))
		r.Msg.PageSize = 10
		result, err := client.ListReadableSourceCollections(ctx, r)
		require.NoError(t, err)
		var ids []string
		for _, collection := range result.Msg.Collections {
			ids = append(ids, collection.SourceId)
		}
		return ids
	}
	// Each module reads what its own content type governs, and nothing else — the
	// rows module must not be handed a documents-governed collection.
	require.Equal(t, []string{sourceA, sourceB}, page("documents"))
	require.Equal(t, []string{sourceR}, page("rows"))
	require.Equal(t, []string{"rows"}, store.resources)
	// A cursor is bound to the resource types it enumerated under, so another
	// module cannot resume a page this one opened.
	first, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "documents", "read")))
	require.NoError(t, err)
	require.NotEmpty(t, first.Msg.NextPageToken)
	next := sourceReadRequest(mint("rows", "rows", "read"))
	next.Msg.PageToken = first.Msg.NextPageToken
	_, err = client.ListReadableSourceCollections(ctx, next)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// A wildcard role permission grants read on every resource type, so it covers
// whatever the caller declared. The declaration is then the only thing bounding
// it, which is why an empty one has to be refused rather than trusted to match
// nothing: '*' matches on its own.
func TestSourceReadWildcardRoleStaysBoundedByTheDeclaration(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	store.wildcard = true
	r := sourceReadRequest(mint("rows", "rows", "read"))
	r.Msg.PageSize = 10
	result, err := client.ListReadableSourceCollections(context.Background(), r)
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 3)
	require.Equal(t, []string{"rows"}, store.resources)
}

// The store's fail-closed guarantee must not rest on the one caller that happens
// to check first, so the business layer refuses an empty declaration itself.
func TestSourceReadRefusesAnEmptyDeclarationBeforeStorage(t *testing.T) {
	store, _, _, _ := sourceReadFixture(t)
	ctx := auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg)
	for _, declared := range [][]string{nil, {}} {
		_, err := service.ReadableSourceCollections(ctx, readOrg, []string{readOwner}, declared,
			business.CollectionMetadataDisclosure{}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	}
	require.Zero(t, store.sourceCalls)
}

// A composition that declares no content for a module authorizes nothing for it,
// rather than falling back to a resource type this host invented.
func TestSourceReadDeniesModulesWithNoDeclaredResources(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		business.ModulePrincipalID("documents"): {Prefix: "documents"},
	})
	_, err := client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "documents", "read")))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Zero(t, store.sourceCalls)
}

// The metadata a collection tree renders must reach an ordinary viewer through
// the module-facing contract, without the organization-admin tenancy
// PermissionService/ListCollectionAccess requires. What bounds it is the
// viewer's own sealed authority, so the projection widens with the capability
// rather than with the caller's role.
func TestSourceReadProjectsCollectionMetadataToANonAdminViewer(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	result, err := client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "documents", "read", "roles:read", "audit:read")))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)
	collection := result.Msg.Collections[0]
	require.Equal(t, sourceA, collection.SourceId)
	require.Equal(t, "Example Handbook", collection.BoundaryLabel)

	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.GrantDisclosure)
	require.Len(t, collection.ReadGrants, 2)
	require.Equal(t, "Example Reader", collection.ReadGrants[0].SubjectLabel)
	require.False(t, collection.ReadGrants[0].Inherited)
	// A grant held at an ancestor confers read just as a direct one does; a tree
	// that listed only direct grants would under-report who can read.
	require.Equal(t, "Example Team", collection.ReadGrants[1].SubjectLabel)
	require.True(t, collection.ReadGrants[1].Inherited)
	require.Equal(t, []string{"documents"}, store.grantResources)

	// The stage says which occurrence `at` records, and the request is reported
	// as its own occurrence rather than folded into one "synced by X at T" line.
	require.Equal(t, gen.SourceSyncStage_SOURCE_SYNC_STAGE_CHANGES_ENQUEUED, collection.Sync.Stage)
	require.Equal(t, changesEnqueuedAt, collection.Sync.At.AsTime())
	require.Equal(t, "0e57a1c", collection.Sync.Revision)
	require.Equal(t, "delivery-1", collection.Sync.Trigger)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.Sync.RequesterDisclosure)
	require.Equal(t, syncRequestedAt, collection.Sync.RequestedAt.AsTime())
	require.Equal(t, "Example Operator", collection.Sync.RequestedByLabel)
}

// Withholding and emptiness are different answers, and a consumer that cannot
// tell them apart renders "nobody can read this" for a collection it simply may
// not inspect.
func TestSourceReadWithholdsDetailsTheCapabilityDoesNotSeal(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	ctx := context.Background()
	withheld, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "documents", "read")))
	require.NoError(t, err)
	collection := withheld.Msg.Collections[0]
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD, collection.GrantDisclosure)
	require.Empty(t, collection.ReadGrants)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD, collection.Sync.RequesterDisclosure)
	require.Nil(t, collection.Sync.RequestedAt)
	require.Empty(t, collection.Sync.RequestedByLabel)
	// The ingest facts are the source's own and are not part of either disclosure.
	require.Equal(t, gen.SourceSyncStage_SOURCE_SYNC_STAGE_CHANGES_ENQUEUED, collection.Sync.Stage)
	require.Zero(t, store.grantCalls)
	require.Zero(t, store.requesterCalls)

	// The two details are sealed independently, so one does not carry the other.
	grantsOnly, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "documents", "read", "roles:read")))
	require.NoError(t, err)
	collection = grantsOnly.Msg.Collections[0]
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.GrantDisclosure)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD, collection.Sync.RequesterDisclosure)
	require.Equal(t, 1, store.grantCalls)
	require.Zero(t, store.requesterCalls)

	// A collection with no grant of its own reports DISCLOSED and an empty set —
	// the answer the withheld case must not be confused with.
	delete(store.collectionGrants, "boundary-a")
	empty, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "documents", "read", "roles:read")))
	require.NoError(t, err)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, empty.Msg.Collections[0].GrantDisclosure)
	require.Empty(t, empty.Msg.Collections[0].ReadGrants)
}

// Metadata follows the page, not the tenant: each page discloses details for the
// collections it actually carries, and a source with no ingest record still
// reports its disclosures so a consumer never has to guess from an absent field.
func TestSourceReadDisclosesMetadataPerPage(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	ctx := context.Background()
	token := mint("documents", "documents", "read", "roles:read", "audit:read")
	first, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Equal(t, sourceA, first.Msg.Collections[0].SourceId)
	require.NotEmpty(t, first.Msg.Collections[0].ReadGrants)

	next := sourceReadRequest(token)
	next.Msg.PageToken = first.Msg.NextPageToken
	second, err := client.ListReadableSourceCollections(ctx, next)
	require.NoError(t, err)
	collection := second.Msg.Collections[0]
	require.Equal(t, sourceB, collection.SourceId)
	require.Equal(t, "Example Policies", collection.BoundaryLabel)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.GrantDisclosure)
	require.Empty(t, collection.ReadGrants)
	require.Equal(t, gen.SourceSyncStage_SOURCE_SYNC_STAGE_UNSPECIFIED, collection.Sync.Stage)
	require.Nil(t, collection.Sync.At)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.Sync.RequesterDisclosure)
	require.Nil(t, collection.Sync.RequestedAt)
	require.Equal(t, 2, store.grantCalls)
	require.Equal(t, 2, store.requesterCalls)
}

// A collection the viewer cannot read is absent from the page, so its metadata
// is never fetched for them however much authority the capability seals over
// grants and the audit trail.
func TestSourceReadNeverDisclosesMetadataForAnInaccessibleCollection(t *testing.T) {
	store, _, client, mint := sourceReadFixture(t)
	store.collectionGrants["boundary-denied"] = []*gen.ReadableCollectionGrant{{SubjectLabel: "Example Outsider", SubjectKind: "principal", RoleName: "reader"}}
	request := sourceReadRequest(mint("documents", "documents", "read", "roles:read", "audit:read"))
	request.Msg.PageSize = 10
	result, err := client.ListReadableSourceCollections(context.Background(), request)
	require.NoError(t, err)
	for _, collection := range result.Msg.Collections {
		require.NotEqual(t, "boundary-denied", collection.BoundaryId)
		for _, grant := range collection.ReadGrants {
			require.NotEqual(t, "Example Outsider", grant.SubjectLabel)
		}
	}
}
