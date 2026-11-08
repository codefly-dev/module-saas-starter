package adapters

import (
	"context"
	"errors"
	"sync"
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

// installFaults is the harness's store with one seam: the next install of a
// named target fails once, as a store failing part-way through a closure's
// convergence does. Every other call is the store's own.
type installFaults struct {
	*infra.PostgresStore
	mu   sync.Mutex
	next map[string]error
}

func (f *installFaults) failOnce(targetID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next == nil {
		f.next = map[string]error{}
	}
	f.next[targetID] = err
}

func (f *installFaults) InstallSolution(ctx context.Context, p *business.InstallSolutionParams) (*gen.Installation, error) {
	f.mu.Lock()
	err, armed := f.next[p.TargetID]
	delete(f.next, p.TargetID)
	f.mu.Unlock()
	if armed {
		return nil, err
	}
	return f.PostgresStore.InstallSolution(ctx, p)
}

// reviewedClosureHarness is what the reviewed-closure cases use of the
// conformance harness.
type reviewedClosureHarness struct {
	ctx         context.Context
	conn        grpc.ClientConnInterface
	signer      *solutionhost.InMemorySigner
	svc         *business.Service
	store       *infra.PostgresStore
	faults      *installFaults
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

// testReviewedClosures holds the reviewed install to the core's closure and
// its resumption.
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

	// D2: a convergence failing part-way resumes from the same review, by
	// the original operation's replay and by RetryInstallation; what an
	// earlier attempt installed is reused, never installed twice.
	t.Run("a convergence failing part-way resumes from the same review", func(t *testing.T) {
		const solution = "conformance-closure-resume"
		modules := []string{"conformance-resume-a", "conformance-resume-b", "conformance-resume-c", "conformance-resume-d"}
		for _, m := range modules {
			deliverModule(t, h.ctx, h.conn, h.signer, conformanceModule{alias: m, installationRole: domain.NoInstallationRole})
		}
		reachingSolution(solution, modules...)
		review, err := h.review(t, alex, solution)
		require.NoError(t, err)
		require.Equal(t, modules, requiredPrincipals(review))
		targetOf := func(module string) string {
			resolved, err := h.svc.ResolveInstallationPrincipal(h.ctx, fixtureOrganization, module)
			require.NoError(t, err)
			return resolved.TargetID
		}
		lost := errors.New("the store lost the connection")
		h.faults.failOnce(targetOf(modules[2]), lost)
		pending, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review)
		require.ErrorContains(t, err, lost.Error())
		require.Equal(t, domain.Pending, pending.Status)
		first := map[string]*gen.Installation{}
		for i, m := range modules {
			installed := h.moduleInstallation(t, fixtureOrganization, m)
			require.Equal(t, i < 2, installed != nil, "after two of four modules, %s installed: %v", m, installed != nil)
			first[m] = installed
		}
		// A fresh review reads the pending attempt's installed modules as
		// reused, and is not refused.
		fresh, err := h.review(t, alex, solution)
		require.NoError(t, err)
		require.Equal(t, first[modules[0]].GetId(), fresh.GetModules()[0].GetReuseInstallationId())

		// The original operation's replay installs the third and fails on
		// the fourth; RetryInstallation then finishes the same attempt.
		h.faults.failOnce(targetOf(modules[3]), lost)
		resumed, err := h.svc.InstallReviewedSolution(h.ctx, alex, fixtureOrganization, review)
		require.ErrorContains(t, err, lost.Error())
		require.Equal(t, pending.ID, resumed.ID)
		require.Equal(t, pending.Revision, resumed.Revision, "the replay is the same attempt")
		require.NotNil(t, h.moduleInstallation(t, fixtureOrganization, modules[2]))
		require.Nil(t, h.moduleInstallation(t, fixtureOrganization, modules[3]))
		_, err = h.svc.RetryInstallation(h.ctx, conformanceAlex, fixtureOrganization, pending.ID)
		require.NoError(t, err)
		active, held := h.coreInstallation(t, alex, solution)
		require.True(t, held)
		require.Equal(t, domain.Active, active.Status)
		require.Equal(t, review.GetApproval().GetOperationId(), active.Consent.OperationID, "the installation activated on the original review")
		for _, m := range modules[:2] {
			now := h.moduleInstallation(t, fixtureOrganization, m)
			require.Equal(t, first[m].GetId(), now.GetId(), "%s was installed again", m)
			require.Equal(t, first[m].GetRevision(), now.GetRevision(), "%s moved its revision on resume", m)
		}
		plan, err := h.store.SolutionInstallationPlan(h.ctx, fixtureOrganization, active.ID)
		require.NoError(t, err)
		require.Len(t, plan.Dependencies, 4)
	})
}
