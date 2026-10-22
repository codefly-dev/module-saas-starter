package adapters

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	corework "github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const actorSolutionAudience = "solution:example-tools"

const actorSource = "source-a"

// addModuleActorBindings gives the fixture's consuming module module_actor
// bindings addressed to a module, a solution and the host's source operations,
// plus one naming a kind its module audience does not serve. The audience
// module serves the kind the first one names.
func addModuleActorBindings(prefix string, authority *solutionhost.Authority, grant map[string]any) {
	switch prefix {
	case "compute":
		authority.ResourceKinds = append(authority.ResourceKinds, "profiles")
		grant["resources"] = []string{"profiles"}
	case "reader":
		authority.Bindings = append(authority.Bindings,
			solutionhost.AuthorityBinding{ID: "act", Audience: "compute", Operations: map[string][]string{"invoke": {"profiles:invoke", "profiles:read"}, "lookup": {"profiles:read"}}},
			solutionhost.AuthorityBinding{ID: "act-unserved", Audience: "compute", Operations: map[string][]string{"invoke": {"tickets:read"}}},
			solutionhost.AuthorityBinding{ID: "act-solution", Audience: actorSolutionAudience, Operations: map[string][]string{"invoke": {"tickets:read"}}},
			solutionhost.AuthorityBinding{ID: "act-source", Audience: business.SourceOperationAudience, Operations: map[string][]string{"invoke": {"datasource.sources:invoke", "datasource.sources:read"}, "lookup": {"datasource.sources:read"}}})
		audiences := grant["operation_audiences"].(map[string]any)
		audiences["act"] = map[string]any{"audience": "compute", "delegation_mode": "module_actor",
			"invoke_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"invoke", "read"}}},
			"lookup_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"read"}}}}
		audiences["act-unserved"] = map[string]any{"audience": "compute", "delegation_mode": "module_actor",
			"invoke_scopes": []any{map[string]any{"resource_kind": "tickets", "actions": []string{"read"}}}}
		audiences["act-solution"] = map[string]any{"audience": actorSolutionAudience, "delegation_mode": "module_actor",
			"invoke_scopes": []any{map[string]any{"resource_kind": "tickets", "actions": []string{"read"}}}}
		audiences["act-source"] = map[string]any{"audience": business.SourceOperationAudience, "delegation_mode": "module_actor",
			"invoke_scopes": []any{map[string]any{"resource_kind": "datasource.sources", "actions": []string{"invoke", "read"}, "resource_ids": []string{actorSource}}},
			"lookup_scopes": []any{map[string]any{"resource_kind": "datasource.sources", "actions": []string{"read"}, "resource_ids": []string{actorSource}}}}
	}
}

// solutionInstallations answers the organization's installation of a solution
// audience; the other seal reads are not on this path.
type solutionInstallations struct {
	business.WorkContextSealStore
	installation string
}

func (s *solutionInstallations) WorkContextSolutionInstallation(_ context.Context, org, binding string) (string, error) {
	if org != readOrg || s.installation == "" || binding != strings.TrimPrefix(actorSolutionAudience, business.SolutionAudiencePrefix) {
		return "", business.ErrWorkContextNoAudienceInstallation
	}
	return s.installation, nil
}

type moduleActorJournal struct {
	business.ActorChainJournal
	hops []business.ActorChainHopInput
}

func (j *moduleActorJournal) AppendActorChainHop(_ context.Context, hop business.ActorChainHopInput) (*business.ActorChainHop, error) {
	j.hops = append(j.hops, hop)
	return &business.ActorChainHop{}, nil
}

func (j *moduleActorJournal) AnyActorChainHopRevoked(context.Context, string, []string) (bool, error) {
	return false, nil
}

type moduleActorCase struct {
	f            *installedReadStore
	server       *WorkContextAuthorityServer
	consumer     *workContextConsumerAuthorityFake
	journal      *moduleActorJournal
	solutions    *solutionInstallations
	parent       *gen.IssuedWorkContext
	moduleToken  string
	moduleClaims *basev0.WorkContextV1
}

func moduleActorFixture(t *testing.T, scopes []*gen.WorkContextScope) moduleActorCase {
	t.Helper()
	f, server := installedReadFixture(t)
	journal := &moduleActorJournal{}
	server.journal = journal
	consumer := &workContextConsumerAuthorityFake{}
	server.consumer = consumer
	solutions := &solutionInstallations{installation: uuid.NewString()}
	service.SetWorkContextSealStore(solutions)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	parent, err := server.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: scopes})
	require.NoError(t, err)
	moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
	require.NoError(t, err)
	module, err := corework.Decode(moduleToken)
	require.NoError(t, err)
	return moduleActorCase{f: f, server: server, consumer: consumer, journal: journal, solutions: solutions, parent: parent, moduleToken: moduleToken, moduleClaims: module.Context()}
}

func (c moduleActorCase) exchange(binding, parent, credential string, lookup bool) (*gen.IssuedWorkContext, error) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, credential))
	return (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(ctx, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: lookup})
}

// revision asks the host whether a child still stands, as a callee does,
// naming the operation binding the child seals.
func (c moduleActorCase) revision(t *testing.T, child *basev0.WorkContextV1) error {
	t.Helper()
	subjects := []*gen.WorkContextRevisionSubject{{PrincipalId: child.OwnerPrincipalId, Scopes: wireScopes(child.AuthorityScopes)}}
	for _, hop := range child.ActorChain {
		subjects = append(subjects, &gen.WorkContextRevisionSubject{PrincipalId: hop.PrincipalId, Scopes: wireScopes(hop.GrantedScopes)})
	}
	binding := child.GetOperationBinding().GetBindingId()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test"))
	_, err := c.server.CheckAuthorizationRevision(ctx, &gen.CheckAuthorizationRevisionRequest{OrgId: child.TenantId, OwnerPrincipalId: child.OwnerPrincipalId, AuthorizationRevision: child.AuthorizationRevision, Subjects: subjects, OperationBindingId: &binding})
	return err
}

func wireScopes(scopes []*basev0.WorkScopeV1) []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, &gen.WorkContextScope{ResourceKind: scope.ResourceKind, Actions: scope.Actions, ResourceIds: scope.ResourceIds})
	}
	return out
}

var moduleActorScopes = []*gen.WorkContextScope{
	{ResourceKind: "runs", Actions: []string{"execute"}},
	{ResourceKind: "profiles", Actions: []string{"invoke", "read"}},
	{ResourceKind: "tickets", Actions: []string{"read"}},
	{ResourceKind: "datasource.sources", Actions: []string{"invoke", "read"}, ResourceIds: []string{actorSource}},
}

func TestModuleActorExchangeNamesTheExchangingModule(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	parentClaims, err := corework.Decode(c.parent.Token)
	require.NoError(t, err)
	parent := parentClaims.Context()

	for _, lookup := range []bool{false, true} {
		issued, err := c.exchange("act", c.parent.Token, c.moduleToken, lookup)
		require.NoError(t, err)
		decoded, err := corework.Decode(issued.Token)
		require.NoError(t, err)
		child := decoded.Context()

		require.Len(t, child.ActorChain, 1)
		hop := child.ActorChain[0]
		// The hop is the module the host authenticated, the same principal that
		// owns the module Work Context it issued that module; no request field
		// names it.
		require.Equal(t, c.moduleClaims.OwnerPrincipalId, hop.PrincipalId)
		require.Equal(t, business.ModulePrincipalID("reader"), hop.PrincipalId)
		require.Equal(t, business.PrincipalKindService, hop.PrincipalKind)
		require.Equal(t, hop.PrincipalId, issued.CurrentActorPrincipalId)
		want := []string{"invoke", "read"}
		if lookup {
			want = []string{"read"}
		}
		require.Len(t, hop.GrantedScopes, 1)
		require.Equal(t, "profiles", hop.GrantedScopes[0].ResourceKind)
		require.ElementsMatch(t, want, hop.GrantedScopes[0].Actions)
		require.True(t, strings.HasPrefix(child.GetOperationBinding().GetBindingId(), "installed-actor/"))

		// Owner, tenant, task and lineage are the parent's, and the child never
		// outlives it.
		require.Equal(t, parent.OwnerPrincipalId, child.OwnerPrincipalId)
		require.Equal(t, business.PrincipalKindHuman, child.GetOwnerPrincipalKind())
		require.Equal(t, parent.TenantId, child.TenantId)
		require.Equal(t, parent.TaskId, child.TaskId)
		require.Equal(t, "compute", child.Audience)
		require.LessOrEqual(t, child.ExpiresAtUnix, parent.ExpiresAtUnix)
		require.LessOrEqual(t, child.ExpiresAtUnix, time.Now().Add(61*time.Second).Unix())

		// A callee's revision check confirms the hop from the binding it names;
		// only the owner is a question for the store.
		require.NoError(t, c.revision(t, child))
		require.Len(t, c.consumer.revisionSubjects, 1)
		require.Equal(t, child.OwnerPrincipalId, c.consumer.revisionSubjects[0].PrincipalID)
	}
	require.Len(t, c.journal.hops, 2, "every module hop is journaled")
	require.Equal(t, business.ModulePrincipalID("reader"), c.journal.hops[0].ActorPrincipalID)

	// The same parent through an ordinary binding keeps its owner-only shape.
	ordinary, err := c.exchange("execution", c.parent.Token, c.moduleToken, false)
	require.NoError(t, err)
	decoded, err := corework.Decode(ordinary.Token)
	require.NoError(t, err)
	require.Empty(t, decoded.Context().ActorChain)
}

func TestModuleActorChildIsTerminal(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	issued, err := c.exchange("act", c.parent.Token, c.moduleToken, false)
	require.NoError(t, err)
	_, err = c.exchange("act", issued.Token, c.moduleToken, false)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	_, err = c.server.ExchangeAudience(caller, &gen.ExchangeWorkContextAudienceRequest{OrgId: readOrg, ParentWorkContextToken: issued.Token, Audience: "reader", AttenuatedScopes: moduleActorScopes})
	require.ErrorContains(t, err, "cannot be transformed")
	actorCaller := stampVerifiedIdentity(context.Background(), business.ModulePrincipalID("reader"), readOrg, auth.Assurance{})
	_, err = c.server.RenewWorkContext(actorCaller, &gen.RenewWorkContextRequest{OrgId: readOrg, ParentWorkContextToken: issued.Token})
	require.ErrorContains(t, err, "cannot be renewed")
}

func TestModuleActorExchangeRefusals(t *testing.T) {
	t.Run("a credential that is not a module's", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		out, err := c.exchange("act", c.parent.Token, c.parent.Token, false)
		require.Error(t, err)
		require.Nil(t, out)
	})
	t.Run("a binding not granted to the caller", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
		parent, err := c.server.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "compute", AuthorityScopes: moduleActorScopes})
		require.NoError(t, err)
		compute, _, err := c.server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("compute"), Tenant: readOrg, InstallationID: c.f.live[business.ModulePrincipalID("compute")].InstallationID})
		require.NoError(t, err)
		out, err := c.exchange("act", parent.Token, compute, false)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Nil(t, out)
	})
	t.Run("a parent that already has a chain", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		c.server.authority = &readExchangeAuthority{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}}
		scopes := []*basev0.WorkScopeV1{{ResourceKind: "profiles", Actions: []string{"invoke", "read"}}}
		token, _ := mintTestCapability(t, c.server, testCapability{Audience: "reader", Tenant: readOrg, Owner: readOwner, Scopes: scopes, Actors: []delegationHop{agentHop("actor-1", "hop-1", scopes...)}})
		out, err := c.exchange("act", token, c.moduleToken, false)
		require.Error(t, err)
		require.Nil(t, out)
	})
	t.Run("a kind its module audience does not serve", func(t *testing.T) {
		c := moduleActorFixture(t, moduleActorScopes)
		out, err := c.exchange("act-unserved", c.parent.Token, c.moduleToken, false)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Nil(t, out)
		require.Empty(t, c.journal.hops)
	})
	t.Run("authority broader than the parent", func(t *testing.T) {
		c := moduleActorFixture(t, []*gen.WorkContextScope{{ResourceKind: "profiles", Actions: []string{"read"}}})
		out, err := c.exchange("act", c.parent.Token, c.moduleToken, false)
		require.Error(t, err)
		require.Nil(t, out)
		require.Empty(t, c.journal.hops)
	})
}

func TestModuleActorSolutionAudienceFollowsItsInstallation(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	issued, err := c.exchange("act-solution", c.parent.Token, c.moduleToken, false)
	require.NoError(t, err)
	decoded, err := corework.Decode(issued.Token)
	require.NoError(t, err)
	child := decoded.Context()
	require.Equal(t, actorSolutionAudience, child.Audience)
	require.Equal(t, c.moduleClaims.OwnerPrincipalId, child.ActorChain[0].PrincipalId)
	require.NoError(t, c.revision(t, child))

	// A reinstall is a new installation: the child exchanged for the old one no
	// longer stands, and a fresh exchange does.
	c.solutions.installation = uuid.NewString()
	require.Equal(t, codes.PermissionDenied, status.Code(c.revision(t, child)))
	_, err = c.exchange("act-solution", c.parent.Token, c.moduleToken, false)
	require.NoError(t, err)

	// Uninstalled: nothing is exchanged for it.
	c.solutions.installation = ""
	out, err := c.exchange("act-solution", c.parent.Token, c.moduleToken, false)
	require.Error(t, err)
	require.Nil(t, out)
}

// presentToSource presents a child to the host's source operation surface, as
// a source call does, and returns the context that call would run under.
func presentToSource(token string) (context.Context, error) {
	headers := http.Header{}
	headers.Set(workContextHeader, token)
	return authenticateSourceOperationContext(context.Background(), headers)
}

type sourceAccessWithdrawn struct {
	business.WorkContextAuthorityStore
}

func (sourceAccessWithdrawn) ResolveWorkContextAuthority(context.Context, string, string, string, []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	return nil, business.NewStoreError(errors.New("no access to the source"), business.ErrTypePermission)
}

func TestModuleActorSourceCallIsThePersonsWithTheModuleActing(t *testing.T) {
	c := moduleActorFixture(t, moduleActorScopes)
	for _, lookup := range []bool{false, true} {
		issued, err := c.exchange("act-source", c.parent.Token, c.moduleToken, lookup)
		require.NoError(t, err)
		decoded, err := corework.Decode(issued.Token)
		require.NoError(t, err)
		child := decoded.Context()
		require.Equal(t, business.SourceOperationAudience, child.Audience)
		require.Equal(t, readOwner, child.OwnerPrincipalId)
		require.Len(t, child.ActorChain, 1)
		require.Equal(t, c.moduleClaims.OwnerPrincipalId, child.ActorChain[0].PrincipalId)
		require.Len(t, child.ActorChain[0].GrantedScopes, 1)
		granted := child.ActorChain[0].GrantedScopes[0]
		require.Equal(t, business.SourceOperationResourceKind, granted.ResourceKind)
		require.Equal(t, []string{actorSource}, granted.ResourceIds)
		action := "invoke"
		if lookup {
			action = "read"
			require.Equal(t, []string{"read"}, granted.Actions)
		}

		// The source surface takes it as the person's call with the module
		// acting, for the one source it names.
		ctx, err := presentToSource(issued.Token)
		require.NoError(t, err)
		require.NoError(t, requireSourceOperationScope(ctx, readOrg, actorSource, action))
		require.Error(t, requireSourceOperationScope(ctx, readOrg, "source-b", action))
	}
	require.Len(t, c.journal.hops, 2)
}

// After one accepted call, each loss of standing refuses the same child at the
// source surface.
func TestModuleActorSourceChildRetiresWithItsStanding(t *testing.T) {
	for name, revoke := range map[string]func(c moduleActorCase){
		"the module's installation is withdrawn": func(c moduleActorCase) {
			c.f.live[business.ModulePrincipalID("reader")].InstallationRevision++
		},
		"the binding is narrowed": func(c moduleActorCase) {
			registry := service.ModulePrincipals()
			grant := registry[business.ModulePrincipalID("reader")]
			audiences := make(map[string]business.ModuleOperationAudience, len(grant.OperationAudiences))
			for id, binding := range grant.OperationAudiences {
				audiences[id] = binding
			}
			narrowed := audiences["act-source"]
			narrowed.InvokeScopes = []business.ModuleOperationScope{{ResourceKind: business.SourceOperationResourceKind, Actions: []string{"read"}, ResourceIDs: []string{actorSource}}}
			narrowed.LookupScopes = narrowed.InvokeScopes
			audiences["act-source"] = narrowed
			grant.OperationAudiences = audiences
			registry[business.ModulePrincipalID("reader")] = grant
			c.f.envelope.Revision = 2
			for _, live := range c.f.live {
				v := uint64(2)
				live.EnvelopeRevision = &v
			}
			service.InstallDerivedModulePrincipals(registry, 2, nil)
		},
		"the person loses access to the source": func(c moduleActorCase) {
			c.server.authority = sourceAccessWithdrawn{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := moduleActorFixture(t, moduleActorScopes)
			issued, err := c.exchange("act-source", c.parent.Token, c.moduleToken, false)
			require.NoError(t, err)
			_, err = presentToSource(issued.Token)
			require.NoError(t, err)
			revoke(c)
			_, err = presentToSource(issued.Token)
			require.Error(t, err)
		})
	}
}
