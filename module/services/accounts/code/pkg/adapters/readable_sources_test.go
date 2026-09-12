package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"google.golang.org/grpc"
	"net/http"
	"net/http/httptest"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

const sourceA = "019f6bf7-0000-7000-8000-000000000001"
const sourceB = "019f6bf7-0000-7000-8000-000000000002"

const readOrg = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
const readOwner = "019f6bf7-5b1c-730d-9687-fe6d4aff31ed"

type readProjectionStore struct {
	business.Store
	grants                  map[string][]*gen.AccessibleScope
	sources                 []*business.DatasourceSource
	err                     error
	scopeCalls, sourceCalls int
	revision                int
	expires                 time.Time
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
func (f *readProjectionStore) ListReadableSourcesPage(_ context.Context, org string, subjects []string, after string, limit int) ([]*gen.ReadableSourceCollection, error) {
	f.scopeCalls++
	f.sourceCalls++
	var out []*gen.ReadableSourceCollection
	for _, source := range f.sources {
		if source.OrgID != org {
			return nil, errors.New("unexpected tenant")
		}
		allowed := true
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
		if source.Provider != "github" {
			return nil, status.Error(codes.Unimplemented, "unsupported source")
		}
		ref := source.Branch
		if ref != "" && !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		out = append(out, &gen.ReadableSourceCollection{SourceId: source.ID, BoundaryId: source.BoundaryNodeID, Origin: source.Provider, Container: source.Repo, Ref: ref, Paths: source.Paths})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceId < out[j].SourceId })
	return out[:min(limit, len(out))], f.err
}

type sourceReadIdentityAuthority struct{ *workContextAuthorityFake }

func (a sourceReadIdentityAuthority) ResolveWorkContextAuthority(ctx context.Context, org, owner, actor string, permissions []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, owner); err != nil {
		return nil, err
	}
	return a.workContextAuthorityFake.ResolveWorkContextAuthority(ctx, org, owner, actor, permissions)
}

func sourceReadFixture(t *testing.T) (*readProjectionStore, *workContextAuthorityFake, accountsv1connect.ModuleCapabilitiesServiceClient, func(string, string) string) {
	t.Helper()
	previousService, previousAuthority := service, workContextSingleton
	t.Cleanup(func() { service, workContextSingleton = previousService, previousAuthority; SetInternalToken("") })
	store := &readProjectionStore{grants: map[string][]*gen.AccessibleScope{readOwner: {{NodeId: "boundary-a", ScopePath: "a"}, {NodeId: "boundary-b", ScopePath: "b"}}}, sources: []*business.DatasourceSource{
		{ID: sourceA, OrgID: readOrg, Provider: "github", Repo: "acme/handbook", BoundaryNodeID: "boundary-a", Branch: "main", Paths: []string{"docs/"}},
		{ID: sourceB, OrgID: readOrg, Provider: "github", Repo: "acme/policies", BoundaryNodeID: "boundary-b", Branch: "release"},
		{ID: "source-denied", OrgID: readOrg, Provider: "github", Repo: "acme/private", BoundaryNodeID: "boundary-denied"},
	}}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
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
	mint := func(audience, action string) string {
		token, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: audience, TenantID: readOrg, OwnerPrincipalID: readOwner,
			TaskID: "019f6bf7-1111-7111-8111-111111111111", SessionID: "019f6bf7-2222-7222-8222-222222222222", AuthorizationRevision: facts.facts.EffectiveRevision(), ReplayPolicy: codefly.WorkContextReplayIdempotent,
			AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{action}}}})
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
	token := mint("documents", "read")
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
		{name: "forged", token: "not-signed", perimeter: true}, {name: "wrong audience", token: mint("other", "read"), perimeter: true},
		{name: "scope attenuation", token: mint("documents", "write"), perimeter: true}, {name: "missing perimeter", token: mint("documents", "read")},
		{name: "ambiguous carrier", token: mint("documents", "read"), perimeter: true, duplicate: true},
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
	result, err := client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "read")))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)
	require.Equal(t, 1, store.scopeCalls)
	store.err = errors.New("source backend unavailable")
	result, err = client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "read")))
	require.Error(t, err)
	require.Nil(t, result)
}
func TestSourceReadIntersectsDelegatedSubjectsAndRejectsUnsupportedSources(t *testing.T) {
	store, _, _, _ := sourceReadFixture(t)
	store.grants["actor"] = []*gen.AccessibleScope{{NodeId: "boundary-b", ScopePath: "b"}}
	r, err := service.ReadableSourceCollections(auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg), readOrg, []string{readOwner, "actor"}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
	require.NoError(t, err)
	require.Len(t, r.Collections, 1)
	require.Equal(t, sourceB, r.Collections[0].SourceId)
	store.sources[1].Provider = "unsupported"
	r, err = service.ReadableSourceCollections(auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg), readOrg, []string{readOwner, "actor"}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
	require.Error(t, err)
	require.Nil(t, r)
	store.sources[1].OrgID = "another-tenant"
	r, err = service.ReadableSourceCollections(auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg), readOrg, []string{readOwner}, &gen.ListReadableSourceCollectionsRequest{}, func(context.Context) error { return nil })
	require.Error(t, err)
	require.Nil(t, r)
}
