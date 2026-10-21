package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type artifactSourcesStore struct {
	*currentInstallationStore
	targetError       error
	installationError error
}

type unavailableArtifactSourceAuthority struct {
	business.WorkContextAuthorityStore
}

func (unavailableArtifactSourceAuthority) ResolveWorkContextAuthority(context.Context, string, string, string, []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	return nil, errors.New("current authority storage unavailable")
}

func (s *artifactSourcesStore) GetInstallation(ctx context.Context, tenant, id string) (*gen.Installation, gen.InstallationHealth, error) {
	if s.installationError != nil {
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, s.installationError
	}
	return s.currentInstallationStore.GetInstallation(ctx, tenant, id)
}

func (s *artifactSourcesStore) GetSolutionTarget(ctx context.Context, id string) (*business.SolutionTarget, error) {
	if s.targetError != nil {
		return nil, s.targetError
	}
	return s.currentInstallationStore.GetSolutionTarget(ctx, id)
}

func TestExecutableArtifactSourcesCredentialRevisionOutage(t *testing.T) {
	for _, principal := range []string{readOwner, business.ModulePrincipalID("example")} {
		t.Run(principal, func(t *testing.T) {
			_, _, client, mint := sourceReadFixture(t)
			installReadBinding()
			parent := mint("example", "results", "read")
			r := connect.NewRequest(&gen.ModuleExecutableArtifactSourcesRequest{ParentWorkContextToken: parent, InstallationId: uuid.NewString(), PolicyId: "content"})
			for k, v := range readExchangeRequest(t, parent).Header() {
				r.Header()[k] = append([]string(nil), v...)
			}
			workContextSingleton.seals.(*sealAnswersFake).revisionErr[principal] = errors.New("revision store unavailable")
			_, err := client.ListExecutableArtifactSources(context.Background(), r)
			require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			require.NotContains(t, err.Error(), "revision store unavailable")
		})
	}
}

func TestExecutableArtifactSourcesCurrentAuthorityOutage(t *testing.T) {
	_, _, client, mint := sourceReadFixture(t)
	installReadBinding()
	parent := mint("example", "results", "read")
	r := connect.NewRequest(&gen.ModuleExecutableArtifactSourcesRequest{ParentWorkContextToken: parent, InstallationId: uuid.NewString(), PolicyId: "content"})
	for k, v := range readExchangeRequest(t, parent).Header() {
		r.Header()[k] = append([]string(nil), v...)
	}
	workContextSingleton.authority = unavailableArtifactSourceAuthority{}
	_, err := client.ListExecutableArtifactSources(context.Background(), r)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "current authority storage")
}

// Real generated gRPC client, served authority interceptor and host signatures;
// owner storage/current revision fixtures are controlled, not a deployed host.
func TestExecutableArtifactSourcesServedCurrentPolicySet(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	id, target := uuid.NewString(), uuid.NewString()
	original := &gen.Installation{Id: id, OrgId: readOrg, TargetId: target, Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE}
	store := &artifactSourcesStore{currentInstallationStore: &currentInstallationStore{
		artifactTransportStore: artifactTransportStore{member: true, rows: map[string]*business.ExecutableArtifactApproval{}},
		installation:           proto.Clone(original).(*gen.Installation),
		target:                 &business.SolutionTarget{ID: target, BindingID: artifactTransportBindingID, SolutionID: "acme/example"},
	}}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	policy := business.ExecutableArtifactPolicy{
		Schema:    "example.artifact/v1",
		Sources:   map[string]string{"acme.beta": artifactTransportBindingID, "acme.alpha": artifactTransportBindingID, "other.source": "other-binding"},
		Activate:  business.ArtifactPermission{Resource: "definitions", Action: "configure"},
		Run:       business.ArtifactPermission{Resource: "definitions", Action: "run"},
		Contracts: []business.ArtifactContract{{Kind: "model", Name: "example/chat", Digest: "sha256:" + strings.Repeat("a", 64)}}, RequiredKinds: []string{"model"},
	}
	principal := business.ModulePrincipalID("example")
	grant := business.ModulePrincipalGrant{Prefix: "example", Tenant: readOrg, ArtifactPolicies: map[string]business.ExecutableArtifactPolicy{"content": policy}}
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{principal: grant})
	installCurrentInstallationCaller(t, service, readOrg)
	parent, signed := singleUseViewer(t, "example", "definitions")
	require.NoError(t, consumeAsTheConsumer(t, signed))
	request := func() *connect.Request[gen.ModuleExecutableArtifactSourcesRequest] {
		r := connect.NewRequest(&gen.ModuleExecutableArtifactSourcesRequest{ParentWorkContextToken: parent, InstallationId: id, PolicyId: "content"})
		for k, v := range readExchangeRequest(t, parent).Header() {
			r.Header()[k] = append([]string(nil), v...)
		}
		return r
	}
	for range 2 {
		out, e := client.ListExecutableArtifactSources(context.Background(), request())
		require.NoError(t, e)
		require.Equal(t, "content", out.Msg.PolicyId)
		require.Equal(t, []string{"acme.alpha", "acme.beta"}, out.Msg.Sources)
		require.Equal(t, id, out.Msg.Installation.InstallationId)
		require.Equal(t, readOrg, out.Msg.Installation.TenantId)
		require.Equal(t, target, out.Msg.Installation.TargetId)
		require.Equal(t, artifactTransportBindingID, out.Msg.Installation.BindingId)
	}
	require.Error(t, consumeAsTheConsumer(t, signed), "the reads neither consume nor remint the already-consumed parent")
	require.Empty(t, store.rows, "metadata observation never creates executable consent")
	store.target.BindingID = "other-binding"
	one, err := client.ListExecutableArtifactSources(context.Background(), request())
	require.NoError(t, err)
	require.Equal(t, []string{"other.source"}, one.Msg.Sources)
	store.target.BindingID = "binding-without-sources"
	zero, err := client.ListExecutableArtifactSources(context.Background(), request())
	require.NoError(t, err)
	require.Empty(t, zero.Msg.Sources)
	store.target.BindingID = artifactTransportBindingID

	for _, tc := range []struct {
		name   string
		change func(*connect.Request[gen.ModuleExecutableArtifactSourcesRequest])
	}{
		{"no perimeter", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) {
			r.Header().Del("x-codefly-internal-token")
		}},
		{"no module", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) {
			r.Header().Del(workContextHeader)
		}},
		{"duplicate module", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) {
			r.Header().Add(workContextHeader, r.Header().Get(workContextHeader))
		}},
		{"viewer is not module", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) {
			r.Header().Set(workContextHeader, parent)
		}},
		{"forged parent", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) {
			r.Msg.ParentWorkContextToken = "forged"
		}},
		{"wrong audience", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) {
			r.Msg.ParentWorkContextToken = mint("other", "definitions", "read")
		}},
		{"invalid installation", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) { r.Msg.InstallationId = "bad" }},
		{"missing policy", func(r *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) { r.Msg.PolicyId = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := store.reads
			r := request()
			tc.change(r)
			_, e := client.ListExecutableArtifactSources(context.Background(), r)
			require.Error(t, e)
			require.Equal(t, before, store.reads)
		})
	}
	unknown := request()
	unknown.Msg.PolicyId = "hidden-or-missing"
	_, denied := client.ListExecutableArtifactSources(context.Background(), unknown)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(denied))
	// The real PostgresStore reports absent/foreign rows as a typed StoreError,
	// not a nil row. It must remain indistinguishable from hidden state.
	for _, kind := range []business.StoreErrorType{business.ErrTypeNotFound, business.ErrTypePermission} {
		store.installationError = business.NewStoreError(errors.New("hidden row"), kind)
		_, err = client.ListExecutableArtifactSources(context.Background(), request())
		require.Equal(t, denied.Error(), err.Error())
	}
	store.installationError = errors.New("storage connection lost")
	_, err = client.ListExecutableArtifactSources(context.Background(), request())
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	store.installationError = nil
	for _, tc := range []struct {
		name   string
		change func(*gen.Installation)
	}{
		{"foreign tenant", func(i *gen.Installation) { i.OrgId = uuid.NewString() }},
		{"foreign installation", func(i *gen.Installation) { i.Id = uuid.NewString() }},
		{"revoked", func(i *gen.Installation) { i.RevokedAt = timestamppb.Now() }},
		{"pending", func(i *gen.Installation) { i.Status = gen.InstallationStatus_INSTALLATION_STATUS_PENDING }},
		{"failed", func(i *gen.Installation) { i.Status = gen.InstallationStatus_INSTALLATION_STATUS_FAILED }},
		{"no target", func(i *gen.Installation) { i.TargetId = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.installation = proto.Clone(original).(*gen.Installation)
			tc.change(store.installation)
			_, e := client.ListExecutableArtifactSources(context.Background(), request())
			require.Equal(t, denied.Error(), e.Error())
		})
	}
	store.installation = proto.Clone(original).(*gen.Installation)
	store.member = false
	_, err = client.ListExecutableArtifactSources(context.Background(), request())
	require.Equal(t, denied.Error(), err.Error())
	store.member = true
	closed := uint64(2)
	store.target.ClosedGeneration = &closed
	_, err = client.ListExecutableArtifactSources(context.Background(), request())
	require.Equal(t, denied.Error(), err.Error())
	store.target.ClosedGeneration = nil

	// A replacement taking the same display alias inherits no permitted sources.
	replacement := uuid.NewString()
	store.installation.TargetId = replacement
	store.target = &business.SolutionTarget{ID: replacement, BindingID: "replacement-binding", SolutionID: "acme/example"}
	out, err := client.ListExecutableArtifactSources(context.Background(), request())
	require.NoError(t, err)
	require.Empty(t, out.Msg.Sources)
	require.Equal(t, "replacement-binding", out.Msg.Installation.BindingId)
	store.targetError = errors.New("storage lookup unavailable")
	_, err = client.ListExecutableArtifactSources(context.Background(), request())
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "storage lookup")
	store.targetError = nil
	facts.facts.OrganizationRevision++
	_, err = client.ListExecutableArtifactSources(context.Background(), request())
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	facts.facts.OrganizationRevision--
	// Current delegated parents may observe metadata, but a revoked delegation
	// refuses before reading the selected installation on the next request.
	workContextSingleton.authority = &readExchangeAuthority{facts: facts.facts}
	journal := &readExchangeJournal{}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "definitions", Actions: []string{"read"}}}
	delegated, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: "example", Tenant: readOrg, Owner: readOwner, Scopes: scopes, Actors: []delegationHop{agentHop("actor-1", "hop-1", scopes...)}})
	r := request()
	r.Msg.ParentWorkContextToken = delegated
	_, err = client.ListExecutableArtifactSources(context.Background(), r)
	require.NoError(t, err)
	before := store.reads
	journal.revoked = "hop-1"
	_, err = client.ListExecutableArtifactSources(context.Background(), r)
	require.Error(t, err)
	require.Equal(t, before, store.reads)
	journal.revoked = ""

	// Removing a policy from the current ceiling cannot read the consented old map.
	current := service.ModulePrincipals()
	g := current[principal]
	g.ArtifactPolicies = nil
	current[principal] = g
	service.SetModulePrincipals(current)
	_, err = client.ListExecutableArtifactSources(context.Background(), request())
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Empty(t, store.rows)
}
