package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

const readOrg = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
const readOwner = "019f6bf7-5b1c-730d-9687-fe6d4aff31ed"

type readProjectionStore struct {
	business.Store
	grants                  map[string][]*gen.AccessibleScope
	sources                 []*business.DatasourceSource
	err                     error
	scopeCalls, sourceCalls int
}

func (f *readProjectionStore) WithOrgTx(ctx context.Context, org string, run func(context.Context) error) error {
	if org != readOrg {
		return errors.New("unexpected tenant")
	}
	return run(ctx)
}
func (f *readProjectionStore) ListAccessibleScopes(_ context.Context, org, subject string, kind gen.SubjectKind, resource, action, after string, limit int) ([]*gen.AccessibleScope, error) {
	f.scopeCalls++
	if org != readOrg || kind != gen.SubjectKind_SUBJECT_KIND_PRINCIPAL || resource != "documents" || action != "read" {
		return nil, errors.New("wrong authorization coordinates")
	}
	if f.err != nil {
		return nil, f.err
	}
	var out []*gen.AccessibleScope
	for _, scope := range f.grants[subject] {
		if scope.ScopePath > after {
			out = append(out, scope)
		}
	}
	return out[:min(len(out), limit)], nil
}
func (f *readProjectionStore) ListDatasourceSources(_ context.Context, org string) ([]*business.DatasourceSource, error) {
	f.sourceCalls++
	if org != readOrg {
		return nil, errors.New("unexpected tenant")
	}
	return f.sources, f.err
}

func sourceReadFixture(t *testing.T) (*readProjectionStore, *workContextAuthorityFake, accountsv1connect.ModuleCapabilitiesServiceClient, func(string, string) string) {
	t.Helper()
	previousService, previousAuthority := service, workContextSingleton
	t.Cleanup(func() { service, workContextSingleton = previousService, previousAuthority; SetInternalToken("") })
	store := &readProjectionStore{grants: map[string][]*gen.AccessibleScope{readOwner: {{NodeId: "boundary-a", ScopePath: "a"}, {NodeId: "boundary-b", ScopePath: "b"}}}, sources: []*business.DatasourceSource{
		{ID: "source-a", OrgID: readOrg, Provider: "github", Repo: "acme/handbook", BoundaryNodeID: "boundary-a", Branch: "main", Paths: []string{"docs/"}},
		{ID: "source-b", OrgID: readOrg, Provider: "github", Repo: "acme/policies", BoundaryNodeID: "boundary-b", Branch: "release"},
		{ID: "source-denied", OrgID: readOrg, Provider: "github", Repo: "acme/private", BoundaryNodeID: "boundary-denied"},
	}}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	facts := &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	workContextSingleton = &WorkContextAuthorityServer{}
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "test-key", PrivateKey: key, Authority: facts})
	SetInternalToken("source-read-test-perimeter")
	_, handler := accountsv1connect.NewModuleCapabilitiesServiceHandler(&moduleCapabilitiesConnectHandler{inner: &ModuleCapabilitiesServer{}})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := accountsv1connect.NewModuleCapabilitiesServiceClient(server.Client(), server.URL)
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
	require.Equal(t, "source-a", first.Msg.Collections[0].SourceId)
	require.Equal(t, "refs/heads/main", first.Msg.Collections[0].Ref)
	require.Equal(t, []string{"docs/"}, first.Msg.Collections[0].Paths)
	next := sourceReadRequest(token)
	next.Msg.PageToken = first.Msg.NextPageToken
	second, err := client.ListReadableSourceCollections(ctx, next)
	require.NoError(t, err)
	require.Equal(t, "source-b", second.Msg.Collections[0].SourceId)
	require.Empty(t, second.Msg.NextPageToken)
	// A current grant deletion invalidates a page even with the original token.
	store.grants[readOwner] = nil
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
	require.Equal(t, 2, store.scopeCalls)
	store.err = errors.New("source backend unavailable")
	result, err = client.ListReadableSourceCollections(context.Background(), sourceReadRequest(mint("documents", "read")))
	require.Error(t, err)
	require.Nil(t, result)
}
func TestSourceReadIntersectsDelegatedSubjectsAndRejectsUnsupportedSources(t *testing.T) {
	store, _, _, _ := sourceReadFixture(t)
	store.grants["actor"] = []*gen.AccessibleScope{{NodeId: "boundary-b", ScopePath: "b"}}
	r, err := service.ReadableSourceCollections(context.Background(), readOrg, []string{readOwner, "actor"}, &gen.ListReadableSourceCollectionsRequest{})
	require.NoError(t, err)
	require.Len(t, r.Collections, 1)
	require.Equal(t, "source-b", r.Collections[0].SourceId)
	store.sources[1].Provider = "unsupported"
	r, err = service.ReadableSourceCollections(context.Background(), readOrg, []string{readOwner, "actor"}, &gen.ListReadableSourceCollectionsRequest{})
	require.Error(t, err)
	require.Nil(t, r)
	store.sources[1].OrgID = "another-tenant"
	r, err = service.ReadableSourceCollections(context.Background(), readOrg, []string{readOwner}, &gen.ListReadableSourceCollectionsRequest{})
	require.Error(t, err)
	require.Nil(t, r)
}
