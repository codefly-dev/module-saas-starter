package adapters

import (
	"context"
	"errors"
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
	carried := func(t *testing.T, issued *gen.IssuedWorkContext) []string {
		t.Helper()
		decoded, err := corework.Decode(issued.GetToken())
		require.NoError(t, err)
		var kinds []string
		for _, sc := range decoded.Context().GetAuthorityScopes() {
			kinds = append(kinds, sc.GetResourceKind())
		}
		return kinds
	}
	refusedByRule := func(t *testing.T, err error, audience string) {
		t.Helper()
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(),
			audience+" checks owners, so an organization role reaches none of its owned data on a person's own session")
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
		exchangeAs := func(module, binding, parent string) (*gen.IssuedWorkContext, error) {
			identity, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(), &gen.ModuleMintWorkContextRequest{
				Prefix: module, ServiceAccountToken: credential(module), OrganizationId: fixtureOrganization})
			require.NoError(t, err)
			return ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(presentedAs(t, identity.GetToken()),
				&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent})
		}
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
	})
}

// composedFrom answers one module, or one error, for any name.
type composedFrom struct {
	module domain.Module
	err    error
}

func (c composedFrom) Composed(context.Context, string) (domain.Module, error) {
	return c.module, c.err
}

// TestOwnSessionRuleFailsClosedOnAnUnreadComposition: the rule applies nowhere
// only where the core composes nothing and this host serves nothing by that
// name; a module this host serves that the core does not compose, or a core
// that cannot be read, refuses rather than reading as one that checks no owner.
func TestOwnSessionRuleFailsClosedOnAnUnreadComposition(t *testing.T) {
	missing := composedFrom{err: domain.Missing("no module named documents")}
	_, composed, err := composedBy(t.Context(), missing, false, "documents")
	require.NoError(t, err)
	require.False(t, composed, "nothing composed and nothing served: no rule applies")

	_, _, err = composedBy(t.Context(), missing, true, "documents")
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "does not compose it yet")

	_, _, err = composedBy(t.Context(), composedFrom{err: errors.New("store unreachable")}, false, "documents")
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)

	m, composed, err := composedBy(t.Context(), composedFrom{module: domain.Module{Name: "documents", OwnerAware: true}}, true, "documents")
	require.NoError(t, err)
	require.True(t, composed)
	require.Equal(t, "documents", m.Name)
}
