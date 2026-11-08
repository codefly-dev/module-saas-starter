package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// reviewedClosureHarness is what the reviewed-closure cases use of the
// conformance harness.
type reviewedClosureHarness struct {
	ctx         context.Context
	conn        grpc.ClientConnInterface
	signer      *solutionhost.InMemorySigner
	svc         *business.Service
	store       *infra.PostgresStore
	core        *authority.Service
	pagePayload *solutionhost.FrontendPayload
}

// review reviews target at org as caller, by its delivered name.
func (h reviewedClosureHarness) review(t *testing.T, caller authority.Person, target string) (*gen.ReviewedSolutionInstall, error) {
	t.Helper()
	resolved, err := h.svc.ResolveInstallationPrincipal(h.ctx, caller.Organization, target)
	require.NoError(t, err)
	return h.svc.ReviewSolutionInstall(h.ctx, caller, caller.Organization, resolved.TargetID)
}

// moduleInstallation is accounts' own installation of a module at org, nil
// when there is none.
func (h reviewedClosureHarness) moduleInstallation(t *testing.T, org, module string) *gen.Installation {
	t.Helper()
	resolved, err := h.svc.ResolveInstallationPrincipal(h.ctx, org, module)
	require.NoError(t, err)
	rows, _, err := h.svc.ListOrganizationInstallations(h.ctx, org, "", 500)
	require.NoError(t, err)
	for _, row := range rows {
		if inst := row.Summary.GetInstallation(); inst.GetTargetId() == resolved.TargetID && inst.GetStatus() == gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
			return inst
		}
	}
	return nil
}

// coreInstallation is the core's installation of target at org, if any.
func (h reviewedClosureHarness) coreInstallation(t *testing.T, caller authority.Person, target string) (domain.Installation, bool) {
	t.Helper()
	all, err := h.core.Installations(h.ctx, caller, caller.Organization)
	require.NoError(t, err)
	for _, inst := range all {
		if inst.Target == target {
			return inst, true
		}
	}
	return domain.Installation{}, false
}

func requiredPrincipals(review *gen.ReviewedSolutionInstall) []string {
	var out []string
	for _, m := range review.GetModules() {
		if m.GetRequired() {
			out = append(out, m.GetPrincipal())
		}
	}
	return out
}

// testReviewedClosures holds the reviewed install to the core's closure.
func testReviewedClosures(t *testing.T, h reviewedClosureHarness) {
	alex := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	// A module page, a roleless module that binds it, and solutions that
	// reach the page by their own edge and through that module's binding.
	const page, support = "conformance-closure-page", "conformance-closure-support"
	entry := wireScope(page+".entry", "read")
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: page, ceilings: []*authorityv1.WorkScopeV1{entry}, installationRole: domain.NoInstallationRole,
		page:  &solutionhost.Frontend{Title: "Closure page", ExposedModule: "./Page", HostContract: 1, Payload: h.pagePayload},
		roles: map[string][]*authorityv1.WorkScopeV1{"installer": {entry}, "viewer": {entry}}})
	deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: support, installationRole: domain.NoInstallationRole,
		ceilings: []*authorityv1.WorkScopeV1{wireScope(support+".item", "read")},
		bindings: []solutionhost.AuthorityBinding{{ID: "page", Audience: page, Operations: map[string][]string{"invoke": {page + ".entry:read"}}}}})
	reachingSolution := func(alias string, edges ...string) {
		note := wireScope(alias+".note", "read")
		s := conformanceSolution{alias: alias, generation: 1, installationRole: domain.NoInstallationRole, ceilings: []*authorityv1.WorkScopeV1{note},
			roles: map[string][]*authorityv1.WorkScopeV1{"installer": {note}, "viewer": {note}}}
		for _, edge := range edges {
			s.edges = append(s.edges, modulecontract.EdgeDependency{Module: edge, Presence: "required"})
		}
		deliverSolution(t, h.ctx, h.conn, h.signer, s)
	}

	// D1, D3: a module page a solution reaches is chosen by its own review.
	// The solution's review lists the roleless module alone and installs
	// without the page, before and after the page is installed.
	t.Run("a solution reaching a module page installs without it, before and after the page is installed", func(t *testing.T) {
		const before, after = "conformance-closure-before", "conformance-closure-after"
		reachingSolution(before, support, page)
		reachingSolution(after, support, page)
		review, err := h.review(t, alex, before)
		require.NoError(t, err)
		require.Equal(t, []string{support}, requiredPrincipals(review), "the review lists the roleless module and not the page")
		inst, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review)
		require.NoError(t, err)
		require.Equal(t, domain.Active, inst.Status)
		_, held := h.coreInstallation(t, alex, page)
		require.False(t, held, "the solution's install installed the module page")

		pageInst, err := installConformanceSolution(h.ctx, h.svc, h.core, page, "", conformanceAlex)
		require.NoError(t, err)
		require.Equal(t, domain.Active, pageInst.Status)
		review, err = h.review(t, alex, after)
		require.NoError(t, err, "a module page installed through its review fails the review of a solution reaching it")
		require.Equal(t, []string{support}, requiredPrincipals(review))
		require.NotEmpty(t, review.GetModules()[0].GetReuseInstallationId(), "the roleless module the first solution installed is reused")
		inst, err = h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review)
		require.NoError(t, err)
		require.Equal(t, domain.Active, inst.Status)
	})
}
