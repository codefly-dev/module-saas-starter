package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/solutionhost"
	corework "github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// testOwnSessionOwnerRule drives accounts' own person Work Context mints in
// the conformance harness's composition: documents checks owners of
// example-records; model checks owners of its datasets and shares its
// profiles; records and org-directory check no owner. An administrator's
// organization RBAC holds every kind, so whatever is refused here is refused
// by the core's owner rule (RBAC-4a), named as the core names it.
//
// credential is a module workload's projected credential; deliver delivers a
// module and installs it at both organizations; page is a module page's
// frontend.
func testOwnSessionOwnerRule(t *testing.T, conn grpc.ClientConnInterface, admin context.Context, credential func(string) string, deliver func(conformanceModule), page *solutionhost.Frontend) {
	contexts := gen.NewWorkContextServiceClient(conn)
	scope := func(kind string, actions ...string) *gen.WorkContextScope {
		return &gen.WorkContextScope{ResourceKind: kind, Actions: actions}
	}
	start := func(audience string, scopes ...*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: audience, AuthorityScopes: scopes})
	}
	carried, refusedByRule := carriedKinds, refusedByOwnerRule
	exchangeAs := func(module, binding, parent string) (*gen.IssuedWorkContext, error) {
		return moduleExchange(t, credential, module, binding, parent)
	}
	// A parent at records, which checks no owner, carrying an owned kind of
	// each module that checks owners and the model's shared profiles.
	parent, err := start("records", scope("example-entries", "read"), scope("example-records", "read"),
		scope("modelservice.datasets", "read"), scope("modelservice.profiles", "read"))
	require.NoError(t, err)

	t.Run("StartTask", func(t *testing.T) {
		_, err := start("documents", scope("example-records", "read"))
		refusedByRule(t, err, "documents")
		_, err = start("model", scope("modelservice.profiles", "read"), scope("modelservice.datasets", "read"))
		refusedByRule(t, err, "model")

		shared, err := start("model", scope("modelservice.profiles", "invoke", "read"))
		require.NoError(t, err, "a kind the module declares shared")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, shared))
		blind, err := start("records", scope("example-entries", "read", "write"))
		require.NoError(t, err, "a module that checks no owner")
		require.Equal(t, []string{"example-entries"}, carried(t, blind))
	})

	t.Run("StartTask through an operation binding", func(t *testing.T) {
		// documents' model binding invokes the profiles and reads the datasets;
		// through a module with no roles the person holds at the model only
		// what their own session holds there.
		issued, err := contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: "documents", OperationBindings: []string{"model"}})
		require.NoError(t, err)
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, issued))
	})

	t.Run("StartRootSession", func(t *testing.T) {
		root := func(token, audience string) (*gen.IssuedWorkContext, error) {
			return contexts.StartRootSession(admin, &gen.StartRootSessionWorkContextRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: token, SessionId: uuid.NewString(), Audience: audience})
		}
		_, err := root(parent.GetToken(), "documents")
		refusedByRule(t, err, "documents")
		_, err = root(parent.GetToken(), "model")
		refusedByRule(t, err, "model")

		profiles, err := start("records", scope("modelservice.profiles", "read"))
		require.NoError(t, err)
		_, err = root(profiles.GetToken(), "model")
		require.NoError(t, err, "a kind the module declares shared")
		_, err = root(parent.GetToken(), "org-directory")
		require.NoError(t, err, "a module that checks no owner")
	})

	t.Run("ExchangeAudience", func(t *testing.T) {
		exchange := func(audience string, scopes ...*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
			return contexts.ExchangeAudience(admin, &gen.ExchangeWorkContextAudienceRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: parent.GetToken(), Audience: audience, AttenuatedScopes: scopes})
		}
		_, err := exchange("documents", scope("example-records", "read"))
		refusedByRule(t, err, "documents")
		_, err = exchange("model", scope("modelservice.datasets", "read"))
		refusedByRule(t, err, "model")

		shared, err := exchange("model", scope("modelservice.profiles", "read"))
		require.NoError(t, err, "a kind the module declares shared")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, shared))
		blind, err := exchange("org-directory", scope("example-entries", "read"), scope("example-records", "read"),
			scope("modelservice.datasets", "read"), scope("modelservice.profiles", "read"))
		require.NoError(t, err, "a module that checks no owner")
		require.ElementsMatch(t, []string{"example-entries", "example-records", "modelservice.datasets", "modelservice.profiles"}, carried(t, blind))
	})

	// A module exchanging the person's own session through its binding: the
	// module declares no roles, so the child carries at the binding's audience
	// only what the person's own session holds there.
	t.Run("a module's binding exchange of a person's own session", func(t *testing.T) {
		// documents' model binding invokes the profiles and reads the datasets.
		projected, err := contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: "documents", OperationBindings: []string{"model"}})
		require.NoError(t, err)
		child, err := exchangeAs("documents", "model", projected.GetToken())
		require.NoError(t, err, "the binding a projection was made for is still usable")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, child))

		named, err := start("documents", scope("modelservice.profiles", "invoke", "read"), scope("modelservice.datasets", "read"))
		require.NoError(t, err)
		child, err = exchangeAs("documents", "model", named.GetToken())
		require.NoError(t, err)
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, child), "a kind named at an audience that does not serve it reaches no owner's data")

		// A module whose one binding reads only an owned kind of the model.
		deliver(conformanceModule{alias: "conformance-dataset-reader", ownerUnaware: true,
			ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-dataset-reader.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.datasets:read"}}}}})
		datasets, err := start("conformance-dataset-reader", scope("modelservice.datasets", "read"))
		require.NoError(t, err)
		_, err = exchangeAs("conformance-dataset-reader", "model", datasets.GetToken())
		refusedByRule(t, err, "model")
	})

	// A role-gated audience is decided by its roles, so the rule leaves it as
	// it was: a module page declaring roles, checking owners of its notes, and
	// reading example records at documents through its binding.
	t.Run("a role-gated audience is exempt", func(t *testing.T) {
		const module = "conformance-owner-page"
		note := wireScope(module+".note", "read")
		deliver(conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{note}, page: page, installationRole: domain.NoInstallationRole,
			roles:    map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}},
			bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:read"}}}}})
		own, err := start(module, scope(module+".note", "read"))
		require.NoError(t, err, "its own owned kind on a person's own session")
		require.Equal(t, []string{module + ".note"}, carried(t, own))

		through, err := contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: module, OperationBindings: []string{"documents"}})
		require.NoError(t, err, "a projection through a role-gated module")
		require.Equal(t, []string{"example-records"}, carried(t, through))
		child, err := exchangeAs(module, "documents", through.GetToken())
		require.NoError(t, err, "the role-gated module's exchange through its binding")
		require.Equal(t, []string{"example-records"}, carried(t, child))
	})
}

// carriedKinds are the kinds an issued capability carries in effect: its last
// hop's granted scopes, or its owner's when the owner acts directly (as
// attenuateEffective reads it).
func carriedKinds(t *testing.T, issued *gen.IssuedWorkContext) []string {
	t.Helper()
	decoded, err := corework.Decode(issued.GetToken())
	require.NoError(t, err)
	effective := decoded.Context().GetAuthorityScopes()
	if chain := decoded.Context().GetActorChain(); len(chain) > 0 {
		effective = chain[len(chain)-1].GetGrantedScopes()
	}
	var kinds []string
	for _, sc := range effective {
		kinds = append(kinds, sc.GetResourceKind())
	}
	return kinds
}

// refusedByOwnerRule: err is the core's owner-rule refusal at audience.
func refusedByOwnerRule(t *testing.T, err error, audience string) {
	t.Helper()
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(),
		audience+" checks owners, so an organization role reaches none of its owned data on a person's own session")
}

// moduleExchange is module exchanging parent through its binding, presenting
// the module identity its workload credential is minted.
func moduleExchange(t *testing.T, credential func(string) string, module, binding, parent string) (*gen.IssuedWorkContext, error) {
	t.Helper()
	identity, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(), &gen.ModuleMintWorkContextRequest{
		Prefix: module, ServiceAccountToken: credential(module), OrganizationId: fixtureOrganization})
	require.NoError(t, err)
	return ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(presentedAs(t, identity.GetToken()),
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent})
}

// installedActorHarness is what the installed-actor case needs of the harness.
type installedActorHarness struct {
	admin      context.Context
	contexts   gen.WorkContextServiceClient
	credential func(string) string
	// grant writes the next envelope revision with principals (a JSON object
	// body keyed by module) and derives the registry from it.
	grant func(principals string)
	// withdraw withdraws a module from the core alone, as a delivery the core
	// has not composed: the host still declares it.
	withdraw func(module string) error
	// registered reports whether the host's module registry carries module.
	registered func(module string) bool
}

// installedActorModule checks no owner and binds twice to the model: once
// for its profiles and datasets, once for its datasets alone. The harness
// delivers and installs it before any envelope is written, since a delivery
// after one derives a registry the committed envelope fences.
var installedActorModule = conformanceModule{alias: "conformance-dataset-actor", ownerUnaware: true,
	ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-dataset-actor.task", "read")},
	bindings: []solutionhost.AuthorityBinding{
		{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.profiles:read", "modelservice.datasets:read"}}},
		{ID: "datasets", Audience: "model", Operations: map[string][]string{"invoke": {"modelservice.datasets:read"}}},
	}}

// testInstalledActorOwnSession: an installed module_actor binding of a module
// that checks no owner, exchanging a person's own session, is a hop held at
// its audience as the ordinary arm's is. The case writes an envelope, which
// becomes every module's ceiling, so the harness runs it last.
func testInstalledActorOwnSession(t *testing.T, h installedActorHarness) {
	module := installedActorModule.alias
	actor := func(kinds ...string) string {
		var scopes []string
		for _, kind := range kinds {
			scopes = append(scopes, fmt.Sprintf(`{"resource_kind":%q,"actions":["read"]}`, kind))
		}
		return `{"audience":"model","delegation_mode":"module_actor","invoke_scopes":[` + strings.Join(scopes, ",") + `]}`
	}
	h.grant(fmt.Sprintf(`%q:{"grant":{"tenant":%q,"resources":[%q],"operation_audiences":{"model":%s,"datasets":%s}}},`+
		`"model":{"grant":{"tenant":%q,"resources":["modelservice.profiles","modelservice.datasets","modelservice.templates"]}}`,
		module, fixtureOrganization, module+".task", actor("modelservice.datasets", "modelservice.profiles"), actor("modelservice.datasets"), fixtureOrganization))

	// The module serves neither kind, so the person's own session carries both there.
	parent, err := h.contexts.StartTask(h.admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: module,
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "modelservice.profiles", Actions: []string{"read"}}, {ResourceKind: "modelservice.datasets", Actions: []string{"read"}}}})
	require.NoError(t, err)
	child, err := moduleExchange(t, h.credential, module, "model", parent.GetToken())
	require.NoError(t, err, "the shared kind is issued through the installed binding")
	require.Equal(t, []string{"modelservice.profiles"}, carriedKinds(t, child))
	_, err = moduleExchange(t, h.credential, module, "datasets", parent.GetToken())
	refusedByOwnerRule(t, err, "model")

	// The core no longer composes the module the host still serves: what it
	// checks is unknown, so a person's own mint there is refused.
	require.NoError(t, h.withdraw(module))
	_, err = h.contexts.StartTask(h.admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: module,
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: module + ".task", Actions: []string{"read"}}}})
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "does not compose it yet")

	// documents is a module the host declares that the envelope above omits
	// from the registry. Withdrawn from the core alone, it is still a module
	// this host serves, so a person's own mint there is refused as well.
	require.False(t, h.registered("documents"), "the last envelope omits documents from the registry")
	_, err = h.contexts.StartTask(h.admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: "documents",
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example-records", Actions: []string{"read"}}}})
	refusedByOwnerRule(t, err, "documents")
	require.NoError(t, h.withdraw("documents"))
	_, err = h.contexts.StartTask(h.admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: "documents",
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example-records", Actions: []string{"read"}}}})
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "does not compose it yet")
}

// TestOwnSessionRuleFailsClosedOnAnUnreadComposition: the rule applies nowhere
// only where the core composes nothing and this host serves nothing by that
// name; a module this host serves that the core does not compose, or a core
// that cannot be read, refuses rather than reading as one that checks no owner.
func TestOwnSessionRuleFailsClosedOnAnUnreadComposition(t *testing.T) {
	for name, missing := range map[string]error{
		"the store's": fmt.Errorf("audience %q: %w", "documents", domain.ErrNotFound),
		"a refusal's": domain.Missing("no module named documents"),
	} {
		_, composed, err := composedBy(domain.Module{}, missing, false, "documents")
		require.NoError(t, err, name)
		require.False(t, composed, "%s: nothing composed and nothing served, no rule applies", name)

		_, _, err = composedBy(domain.Module{}, missing, true, "documents")
		require.Equal(t, codes.Unavailable, status.Code(err), "%s: %v", name, err)
		require.Contains(t, status.Convert(err).Message(), "does not compose it yet")
	}

	_, _, err := composedBy(domain.Module{}, errors.New("store unreachable"), false, "documents")
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)

	m, composed, err := composedBy(domain.Module{Name: "documents", OwnerAware: true}, nil, true, "documents")
	require.NoError(t, err)
	require.True(t, composed)
	require.Equal(t, "documents", m.Name)
}
