package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	discoveryOrg    = "11111111-1111-4111-8111-111111111111"
	discoveryOwner  = "22222222-2222-4222-8222-222222222222"
	discoverySource = "33333333-3333-4333-8333-333333333333"
	discoveryOther  = "44444444-4444-4444-8444-444444444444"
)

// The transport and signed capability verification are real. These fixtures
// replace storage, current authority facts and seals; no database or provider
// is exercised and this is not a composed consuming application.
type sourceDiscoveryStore struct {
	sourceWireStore
	mu               sync.Mutex
	readable         bool
	member           bool
	declarationReads atomic.Int64
}

func (s *sourceDiscoveryStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.member {
		return nil, nil
	}
	return &gen.OrgMembership{Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
}

func (s *sourceDiscoveryStore) CanReadScopeNode(ctx context.Context, org, actor string, _ gen.SubjectKind, _, _, _ string) (bool, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, actor); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readable, nil
}

func (s *sourceDiscoveryStore) ListSourceOperations(ctx context.Context, org, source string) ([]operations.Declaration, error) {
	s.declarationReads.Add(1)
	return s.sourceWireStore.ListSourceOperations(ctx, org, source)
}

func sourceDiscoveryFixture(t *testing.T) (*sourceDiscoveryStore, *sealAnswersFake, accountsv1connect.DatasourceServiceClient, func(testCapability) string) {
	t.Helper()
	declaration, err := operations.Admit(connectortest.OperationDeclaration())
	require.NoError(t, err)
	store := &sourceDiscoveryStore{member: true, readable: true, sourceWireStore: sourceWireStore{
		declaration: declaration,
		source:      &business.DatasourceSource{ID: discoverySource, OrgID: discoveryOrg, BoundaryNodeID: "boundary"},
	}}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	previousService, previousAuthority, previousCache := service, workContextSingleton, orgMembershipCache
	t.Cleanup(func() {
		service, workContextSingleton, orgMembershipCache = previousService, previousAuthority, previousCache
	})
	service, orgMembershipCache = svc, nil
	seals := newSealAnswersFake(1)
	workContextSingleton = newCoreTestServer(t, sourceReadIdentityAuthority{&workContextAuthorityFake{
		facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 1},
	}}, seals)
	mux := http.NewServeMux()
	path, handler := accountsv1connect.NewDatasourceServiceHandler(&datasourceConnectHandler{svc: svc}, connect.WithInterceptors(connectAuthInterceptor(nil)))
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := accountsv1connect.NewDatasourceServiceClient(server.Client(), server.URL)
	mint := func(c testCapability) string {
		if c.Audience == "" {
			c.Audience = business.SourceOperationAudience
		}
		if c.Tenant == "" {
			c.Tenant = discoveryOrg
		}
		if c.Owner == "" {
			c.Owner = discoveryOwner
		}
		if c.Scopes == nil {
			c.Scopes = []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: []string{"read"}, ResourceIds: []string{discoverySource}}}
		}
		token, _ := mintTestCapability(t, workContextSingleton, c)
		return token
	}
	return store, seals, client, mint
}

func discoveryRequest(token string) *connect.Request[gen.ListSourceOperationsRequest] {
	req := connect.NewRequest(&gen.ListSourceOperationsRequest{OrgId: discoveryOrg, SourceId: discoverySource})
	if token != "" {
		req.Header().Set(workContextHeader, token)
	}
	return req
}

func TestSourceOperationDiscoveryConnectAuthority(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability testCapability
		change     func(*sourceDiscoveryStore, *connect.Request[gen.ListSourceOperationsRequest])
		code       connect.Code
	}{
		{name: "exact read"},
		{name: "another source", change: func(_ *sourceDiscoveryStore, r *connect.Request[gen.ListSourceOperationsRequest]) {
			r.Msg.SourceId = discoveryOther
		}, code: connect.CodePermissionDenied},
		{name: "another tenant", change: func(_ *sourceDiscoveryStore, r *connect.Request[gen.ListSourceOperationsRequest]) {
			r.Msg.OrgId = discoveryOther
		}, code: connect.CodePermissionDenied},
		{name: "invoke is not read", capability: testCapability{Scopes: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: []string{"invoke"}, ResourceIds: []string{discoverySource}}}}, code: connect.CodePermissionDenied},
		{name: "empty scopes", capability: testCapability{Scopes: []*basev0.WorkScopeV1{}}, code: connect.CodePermissionDenied},
		{name: "unrelated scopes", capability: testCapability{Scopes: []*basev0.WorkScopeV1{{ResourceKind: "example.items", Actions: []string{"read"}, ResourceIds: []string{discoverySource}}}}, code: connect.CodePermissionDenied},
		{name: "unconstrained source", capability: testCapability{Scopes: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: []string{"read"}}}}, code: connect.CodePermissionDenied},
		{name: "wrong audience", capability: testCapability{Audience: "example"}, code: connect.CodeUnauthenticated},
		{name: "module identity is not source authority", capability: testCapability{Audience: ModuleWorkContextAudience, Owner: "module:example", OwnerKind: business.PrincipalKindService, Scopes: []*basev0.WorkScopeV1{}}, code: connect.CodeUnauthenticated},
		{name: "expired credential", change: func(_ *sourceDiscoveryStore, _ *connect.Request[gen.ListSourceOperationsRequest]) {
			workContextSingleton.now = func() time.Time { return time.Now().Add(time.Hour) }
		}, code: connect.CodeUnauthenticated},
		{name: "single use", capability: testCapability{Replay: workcontext.ReplaySingleUse}, code: connect.CodePermissionDenied},
		{name: "no membership", change: func(s *sourceDiscoveryStore, _ *connect.Request[gen.ListSourceOperationsRequest]) { s.member = false }, code: connect.CodePermissionDenied},
		{name: "no source access", change: func(s *sourceDiscoveryStore, _ *connect.Request[gen.ListSourceOperationsRequest]) { s.readable = false }, code: connect.CodePermissionDenied},
		{name: "other person's source", change: func(s *sourceDiscoveryStore, _ *connect.Request[gen.ListSourceOperationsRequest]) {
			s.source.PersonalOwnerUserID = discoveryOther
		}, code: connect.CodePermissionDenied},
		{name: "invalid credential", change: func(_ *sourceDiscoveryStore, r *connect.Request[gen.ListSourceOperationsRequest]) {
			r.Header().Set(workContextHeader, "invalid")
		}, code: connect.CodeUnauthenticated},
		{name: "duplicate credential", change: func(_ *sourceDiscoveryStore, r *connect.Request[gen.ListSourceOperationsRequest]) {
			r.Header().Add(workContextHeader, r.Header().Get(workContextHeader))
		}, code: connect.CodeUnauthenticated},
		{name: "missing credential with forged identity", change: func(_ *sourceDiscoveryStore, r *connect.Request[gen.ListSourceOperationsRequest]) {
			r.Header().Del(workContextHeader)
			r.Header().Set("X-User-Id", discoveryOwner)
			r.Header().Set("X-Org-Id", discoveryOrg)
		}, code: connect.CodeUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, client, mint := sourceDiscoveryFixture(t)
			req := discoveryRequest(mint(tc.capability))
			if tc.change != nil {
				tc.change(store, req)
			}
			out, err := client.ListSourceOperations(t.Context(), req)
			if tc.code != 0 {
				require.Equal(t, tc.code, connect.CodeOf(err), "%v", err)
				require.Nil(t, out)
				require.Zero(t, store.declarationReads.Load(), "refused request read declarations")
				return
			}
			require.NoError(t, err)
			require.Len(t, out.Msg.Operations, 1)
			expected, err := declarationToProto(store.declaration)
			require.NoError(t, err)
			require.True(t, proto.Equal(expected, out.Msg.Operations[0]), "discovery must preserve the admitted declaration")
			require.EqualValues(t, 1, store.declarationReads.Load())
		})
	}
}

func TestSourceOperationDiscoveryRechecksAfterSuccessfulRead(t *testing.T) {
	for _, revoke := range []string{"source permission", "membership", "capability revision"} {
		t.Run(revoke, func(t *testing.T) {
			store, seals, client, mint := sourceDiscoveryFixture(t)
			token := mint(testCapability{})
			_, err := client.ListSourceOperations(t.Context(), discoveryRequest(token))
			require.NoError(t, err)
			switch revoke {
			case "source permission":
				store.mu.Lock()
				store.readable = false
				store.mu.Unlock()
			case "membership":
				store.mu.Lock()
				store.member = false
				store.mu.Unlock()
			default:
				seals.setRevision(2)
			}
			_, err = client.ListSourceOperations(t.Context(), discoveryRequest(token))
			require.Error(t, err, "a prior listing must convey no continuing authority")
			require.EqualValues(t, 1, store.declarationReads.Load())
		})
	}
}

func TestSourceOperationDiscoveryHandlerUsesFinalActorScope(t *testing.T) {
	store, _, _, _ := sourceDiscoveryFixture(t)
	ownerScope := &basev0.WorkScopeV1{ResourceKind: "datasource.sources", Actions: []string{"read"}, ResourceIds: []string{discoverySource, discoveryOther}}
	actorScope := &basev0.WorkScopeV1{ResourceKind: "datasource.sources", Actions: []string{"read"}, ResourceIds: []string{discoverySource}}
	_, claims := mintTestCapability(t, workContextSingleton, testCapability{Audience: business.SourceOperationAudience, Tenant: discoveryOrg, Owner: discoveryOwner,
		Scopes: []*basev0.WorkScopeV1{ownerScope}, Actors: []delegationHop{agentHop("example-agent", "example-delegation", actorScope)}})
	// The handler receives verified identity/claims at its trust seam. Actor
	// admission is outside this test; it must still honor the final attenuation.
	ctx := stampVerifiedIdentity(t.Context(), discoveryOwner, discoveryOrg, auth.Assurance{})
	ctx = context.WithValue(ctx, sourceOperationContextKey{}, claims)
	req := discoveryRequest("")
	req.Msg.SourceId = discoveryOther
	_, err := (&datasourceConnectHandler{svc: service}).ListSourceOperations(ctx, req)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Zero(t, store.declarationReads.Load())
	req.Msg.SourceId = discoverySource
	_, err = (&datasourceConnectHandler{svc: service}).ListSourceOperations(ctx, req)
	require.NoError(t, err)
}

func TestSourceOperationDiscoveryInteractiveReadRemainsAvailable(t *testing.T) {
	store, _, _, _ := sourceDiscoveryFixture(t)
	ctx := stampVerifiedIdentity(t.Context(), discoveryOwner, discoveryOrg, auth.Assurance{})
	out, err := (&datasourceConnectHandler{svc: service}).ListSourceOperations(ctx, discoveryRequest(""))
	require.NoError(t, err)
	require.Len(t, out.Msg.Operations, 1)
	require.EqualValues(t, 1, store.declarationReads.Load())
}

func TestSourceOperationDiscoveryHandlerAttenuatesVerifiedContext(t *testing.T) {
	store, _, _, mint := sourceDiscoveryFixture(t)
	headers := http.Header{}
	headers.Set(workContextHeader, mint(testCapability{}))
	ctx, err := authenticateSourceOperationContext(t.Context(), headers)
	require.NoError(t, err)
	req := discoveryRequest("")
	req.Msg.SourceId = discoveryOther
	_, err = (&datasourceConnectHandler{svc: service}).ListSourceOperations(ctx, req)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Zero(t, store.declarationReads.Load())
}

func TestSourceOperationDiscoveryHandlerBindsBorrowedSource(t *testing.T) {
	// The binding legitimately names two sources, but the signed capability
	// borrows authority from only one. The production verifier must retain that
	// distinction for the read handler rather than relying on scope IDs alone.
	principals := `{"docstore":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},` +
		`"tenant":"` + moduleWorkContextTenant + `","cross_tenant":true,"operation_audiences":{` +
		`"source-sync":{"audience":"` + business.SourceOperationAudience + `",` +
		`"invoke_scopes":[{"resource_kind":"datasource.sources","actions":["invoke","read"],"resource_ids":["` + discoverySource + `","` + discoveryOther + `"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"datasource.sources","actions":["invoke","read"],"resource_ids":["` + discoverySource + `","` + discoveryOther + `"]}]}}}}`
	installDelegationServiceWith(t, principals, discoverySource)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{SourceID: discoverySource})
	require.NoError(t, err)
	claims := verifySourceContext2(t, minted.GetToken())
	require.ElementsMatch(t, []string{discoverySource, discoveryOther}, claims.GetAuthorityScopes()[0].GetResourceIds())
	headers := http.Header{}
	headers.Set(workContextHeader, minted.GetToken())
	ctx, err := authenticateSourceOperationContext(t.Context(), headers)
	require.NoError(t, err)
	// Only storage is replaced after production authentication established the
	// borrowed-source context. No hand-stamped source delegation key is used.
	store, _, _, _ := sourceDiscoveryFixture(t)
	store.source.OrgID = delegationOrg
	req := discoveryRequest("")
	req.Msg.OrgId = delegationOrg
	_, err = (&datasourceConnectHandler{svc: service}).ListSourceOperations(ctx, req)
	require.NoError(t, err)
	req.Msg.SourceId = discoveryOther
	_, err = (&datasourceConnectHandler{svc: service}).ListSourceOperations(ctx, req)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.EqualValues(t, 1, store.declarationReads.Load())
}
