package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type currentInstallationStore struct {
	artifactTransportStore
	installation     *gen.Installation
	reads            int
	tenant, selector string
}

func (s *currentInstallationStore) GetInstallation(_ context.Context, tenant, id string) (*gen.Installation, gen.InstallationHealth, error) {
	s.reads++
	s.tenant, s.selector = tenant, id
	if s.installation == nil {
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, nil
	}
	return proto.Clone(s.installation).(*gen.Installation), gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, nil
}

func TestCurrentInstallationServedModuleAndParent(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	id := uuid.NewString()
	original := &gen.Installation{Id: id, OrgId: readOrg, SolutionIdentifier: "acme/example", Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE}
	store := &currentInstallationStore{artifactTransportStore: artifactTransportStore{member: true}, installation: proto.Clone(original).(*gen.Installation)}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	registry := business.ModulePrincipalRegistry{business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg}}
	service.SetModuleCapabilities(nil, nil, registry)
	parent := mint("example", "definitions", "read")
	request := func(token string) *connect.Request[gen.ModuleCurrentInstallationRequest] {
		r := connect.NewRequest(&gen.ModuleCurrentInstallationRequest{InstallationId: id, ParentWorkContextToken: token})
		for k, v := range readExchangeRequest(t, token).Header() {
			r.Header()[k] = append([]string{}, v...)
		}
		require.Empty(t, r.Header().Get("Authorization"))
		return r
	}
	out, err := client.GetCurrentInstallation(context.Background(), request(parent))
	require.NoError(t, err)
	require.Equal(t, id, out.Msg.InstallationId)
	require.Equal(t, readOrg, out.Msg.TenantId)
	require.Equal(t, "acme/example", out.Msg.SolutionIdentifier)
	require.Equal(t, readOrg, store.tenant)
	require.Equal(t, id, store.selector)
	require.Equal(t, 3, out.Msg.ProtoReflect().Descriptor().Fields().Len(), "identity-only response")
	require.Equal(t, 2, request(parent).Msg.ProtoReflect().Descriptor().Fields().Len(), "selector and proof only; no tenant or source input")
	for _, tc := range []struct {
		name   string
		change func(*connect.Request[gen.ModuleCurrentInstallationRequest])
	}{
		{"no perimeter", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) {
			r.Header().Del("x-codefly-internal-token")
		}},
		{"no module", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) {
			r.Header().Del(workcontext.WorkContextHeaderName)
		}},
		{"viewer parent is not module", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) {
			r.Header().Set(workcontext.WorkContextHeaderName, parent)
		}},
		{"duplicate module", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) {
			r.Header().Add(workcontext.WorkContextHeaderName, r.Header().Get(workcontext.WorkContextHeaderName))
		}},
		{"forged parent", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) {
			r.Msg.ParentWorkContextToken = "forged"
		}},
		{"wrong audience", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) {
			r.Msg.ParentWorkContextToken = mint("other", "definitions", "read")
		}},
		{"bad selector", func(r *connect.Request[gen.ModuleCurrentInstallationRequest]) { r.Msg.InstallationId = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := store.reads
			r := request(parent)
			tc.change(r)
			_, e := client.GetCurrentInstallation(context.Background(), r)
			require.Error(t, e)
			require.Equal(t, before, store.reads)
		})
	}
	store.member = false
	_, err = client.GetCurrentInstallation(context.Background(), request(parent))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	store.member = true
	facts.facts.OrganizationRevision++
	_, err = client.GetCurrentInstallation(context.Background(), request(parent))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	facts.facts.OrganizationRevision--
	for _, tc := range []struct {
		name   string
		change func(*gen.Installation)
	}{
		{"wrong tenant", func(i *gen.Installation) { i.OrgId = uuid.NewString() }},
		{"wrong installation", func(i *gen.Installation) { i.Id = uuid.NewString() }},
		{"revoked", func(i *gen.Installation) { i.RevokedAt = timestamppb.Now() }},
		{"inactive", func(i *gen.Installation) { i.Status = gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED }},
		{"empty source", func(i *gen.Installation) { i.SolutionIdentifier = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.installation = proto.Clone(original).(*gen.Installation)
			tc.change(store.installation)
			_, e := client.GetCurrentInstallation(context.Background(), request(parent))
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(e))
		})
	}
	store.installation = nil
	_, err = client.GetCurrentInstallation(context.Background(), request(parent))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	store.installation = proto.Clone(original).(*gen.Installation)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{})
	_, err = client.GetCurrentInstallation(context.Background(), request(parent))
	require.Error(t, err)
}

func TestCurrentInstallationDelegationCurrentChecks(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	id := uuid.NewString()
	store := &currentInstallationStore{artifactTransportStore: artifactTransportStore{member: true}, installation: &gen.Installation{Id: id, OrgId: readOrg, SolutionIdentifier: "acme/example", Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE}}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg}})
	authority := &readExchangeAuthority{facts: facts.facts}
	workContextSingleton.authority = authority
	journal := &readExchangeJournal{}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "definitions", Actions: []string{"read"}}}
	actors := []*basev0.WorkActorV1{{PrincipalId: "actor-1", PrincipalKind: "service", DelegationId: "hop-1", GrantedScopes: scopes}}
	token, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{Audience: "example", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: "task", SessionID: "session", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: scopes, ActorChain: actors})
	require.NoError(t, err)
	invoke := func() error {
		req := connect.NewRequest(&gen.ModuleCurrentInstallationRequest{InstallationId: id, ParentWorkContextToken: token.Encoded()})
		for k, v := range readExchangeRequest(t, token.Encoded()).Header() {
			req.Header()[k] = v
		}
		_, e := client.GetCurrentInstallation(context.Background(), req)
		return e
	}
	require.NoError(t, invoke())
	require.Equal(t, []string{"hop-1"}, journal.ids)
	before := store.reads
	journal.revoked = "hop-1"
	require.Error(t, invoke())
	require.Equal(t, before, store.reads)
	journal.revoked = ""
	workContextSingleton.journal = nil
	require.Error(t, invoke())
	require.Equal(t, before, store.reads)
	workContextSingleton.journal = journal
	facts.facts.OrganizationRevision++
	require.Error(t, invoke())
	require.Equal(t, before, store.reads)
}
