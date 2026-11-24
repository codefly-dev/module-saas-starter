package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	solutionhost "github.com/codefly-dev/authority/libraries/solutionhost/go"
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
// the conformance harness's composition: documents and conformance-agent
// declare no roles; records, org-directory and model are module pages
// declaring roles, and the administrator holds the admin role on each. The
// administrator also holds every kind asked here by an organisation role the
// fixture grants, so whatever is refused here is refused by the core's rule
// that an organisation role reaches no module, named as the core names it.
//
// credential is a module workload's projected credential; deliver delivers a
// module and installs it at both organizations; page is a module page's
// frontend; seat grants the administrator a role on a module page's
// installation.
func testOwnSessionOwnerRule(t *testing.T, conn grpc.ClientConnInterface, admin context.Context, credential func(string) string, deliver func(conformanceModule), page *solutionhost.Frontend, seat func(target, role string)) {
	contexts := gen.NewWorkContextServiceClient(conn)
	scope := func(kind string, actions ...string) *gen.WorkContextScope {
		return &gen.WorkContextScope{ResourceKind: kind, Actions: actions}
	}
	start := func(audience string, scopes ...*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: audience, AuthorityScopes: scopes})
	}
	carried, refusedByRule := carriedKinds, refusedAtNoRoles
	exchangeAs := func(module, binding, parent string) (*gen.IssuedWorkContext, error) {
		return moduleExchange(t, credential, module, binding, parent)
	}
	// A parent at records, a module page the administrator holds the admin
	// role on, carrying its own kind and, projected through its binding to
	// the model, the model's profiles the admin role holds there. A kind of
	// another module is carried no other way: named outright it is refused,
	// whatever organisation role names it.
	parent, err := contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: "records",
		AuthorityScopes: []*gen.WorkContextScope{scope("example-entries", "read")}, OperationBindings: []string{"model"}})
	require.NoError(t, err)
	require.Equal(t, []string{"example-entries", "modelservice.profiles"}, carried(t, parent))

	t.Run("StartTask", func(t *testing.T) {
		_, err := start("documents", scope("example-records", "read"))
		refusedByRule(t, err, "documents")
		_, err = start("conformance-agent", scope("conformance-agent.conversation", "read"))
		refusedByRule(t, err, "conformance-agent")

		held, err := start("records", scope("example-entries", "read", "write"))
		require.NoError(t, err, "a module page, on the role held on its installation")
		require.Equal(t, []string{"example-entries"}, carried(t, held))
		shared, err := start("model", scope("modelservice.profiles", "invoke", "read"))
		require.NoError(t, err, "what the model's admin role holds")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, shared))
		_, err = start("model", scope("modelservice.profiles", "read"), scope("modelservice.datasets", "read"))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "the model's admin role names no dataset, whatever the organisation role does: %v", err)

		// Another module's kind named outright at a module page: the core
		// issues a person there the kinds the page serves and never signs a
		// role's reach onto a capability addressed to the page, so it is
		// refused as the core refuses it, beside the page's own kind and
		// alone, though the admin role on records names the model's profiles
		// and an organisation role names every kind asked.
		for _, foreign := range []string{"modelservice.profiles", "modelservice.datasets", "example-records"} {
			_, err = start("records", scope("example-entries", "read"), scope(foreign, "read"))
			refusedOutright(t, err, foreign+" named outright at records, beside its own kind")
			_, err = start("records", scope(foreign, "read"))
			refusedOutright(t, err, foreign+" named outright at records")
		}
	})

	t.Run("StartTask through an operation binding", func(t *testing.T) {
		// documents declares no roles: a person's own session holds nothing
		// there, so nothing is projected through its binding either.
		_, err := contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: "documents", OperationBindings: []string{"model"}})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("StartRootSession", func(t *testing.T) {
		root := func(token, audience string) (*gen.IssuedWorkContext, error) {
			return contexts.StartRootSession(admin, &gen.StartRootSessionWorkContextRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: token, SessionId: uuid.NewString(), Audience: audience})
		}
		_, err := root(parent.GetToken(), "documents")
		refusedByRule(t, err, "documents")
		_, err = root(parent.GetToken(), "model")
		require.NoError(t, err, "the model's profiles, which the role on the model's installation holds")
		_, err = root(parent.GetToken(), "org-directory")
		require.NoError(t, err, "a module page the person holds a role on; the kinds it does not serve are inert there")
	})

	t.Run("ExchangeAudience", func(t *testing.T) {
		exchange := func(audience string, scopes ...*gen.WorkContextScope) (*gen.IssuedWorkContext, error) {
			return contexts.ExchangeAudience(admin, &gen.ExchangeWorkContextAudienceRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: parent.GetToken(), Audience: audience, AttenuatedScopes: scopes})
		}
		_, err := exchange("documents", scope("example-records", "read"))
		refusedByRule(t, err, "documents")
		_, err = exchange("model", scope("modelservice.datasets", "read"))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

		shared, err := exchange("model", scope("modelservice.profiles", "read"))
		require.NoError(t, err, "what the role on the model's installation holds")
		require.Equal(t, []string{"modelservice.profiles"}, carried(t, shared))
	})

	// A module that declares no roles exchanging a person's own session
	// through its binding carries nothing of the person's own to a module
	// that declares no roles either.
	t.Run("a module's binding exchange of a person's own session", func(t *testing.T) {
		// A module whose one binding reads a kind documents serves; both
		// declare no roles. The person's own session is not minted at it.
		deliver(conformanceModule{alias: "conformance-record-reader", ownerUnaware: true,
			ceilings: []*authorityv1.WorkScopeV1{wireScope("conformance-record-reader.task", "read")},
			bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:read"}}}}})
		_, err := start("conformance-record-reader", scope("example-records", "read"))
		refusedByRule(t, err, "conformance-record-reader")
	})

	// A role-gated audience is decided by its roles: a module page declaring
	// roles, checking owners of its notes, and reading example records at
	// documents through its binding. Without a role on its installation the
	// administrator is refused there; with one, issued what the role holds.
	t.Run("a role-gated audience is decided by the role held on its installation", func(t *testing.T) {
		const module = "conformance-owner-page"
		note := wireScope(module+".note", "read")
		deliver(conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{note}, page: page, installationRole: domain.NoInstallationRole,
			roles:    map[string][]*authorityv1.WorkScopeV1{"admin": {note, wireScope("example-records", "read")}, "user": {note}},
			bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: map[string][]string{"invoke": {"example-records:read"}}}}})
		_, err := start(module, scope(module+".note", "read"))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), "holds no role on "+module, "an organisation role, an administrator's included, reaches no module page")

		seat(module, domain.AdminRole)
		own, err := start(module, scope(module+".note", "read"))
		require.NoError(t, err, "its own kind, on the role held on its installation")
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

// refusedOutright: err is the core's refusal of a kind an audience does not
// serve, asked of it for a person who holds a role there.
func refusedOutright(t *testing.T, err error, why string) {
	t.Helper()
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", why, err)
	require.Contains(t, status.Convert(err).Message(), "asked beyond what the owner holds", why)
}

// refusedAtNoRoles: err is the core's refusal of a person's own session at
// audience, a module that declares no roles.
func refusedAtNoRoles(t *testing.T, err error, audience string) {
	t.Helper()
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(),
		audience+" declares no roles, so a person's own session holds nothing there")
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

// testInstalledActorOwnSession: a module that declares no roles, with an
// installed module_actor binding, holds nothing for a person's own session,
// so no person's capability exists there for its binding to exchange; and a
// module this host serves that the core no longer composes refuses a person's
// own mint as unknown rather than reading as one the rule leaves alone. The
// case writes an envelope, which becomes every module's ceiling, so the
// harness runs it last.
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

	// The module declares no roles: a person's own session holds nothing there.
	_, err := h.contexts.StartTask(h.admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: module,
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "modelservice.profiles", Actions: []string{"read"}}, {ResourceKind: "modelservice.datasets", Actions: []string{"read"}}}})
	refusedAtNoRoles(t, err, module)

	// The core no longer composes the module the host still serves: what it
	// declares is unknown, so a person's own mint there is refused.
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
	refusedAtNoRoles(t, err, "documents")
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
