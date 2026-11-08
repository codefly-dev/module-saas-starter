//go:build !pure

package infra_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// An installation's consent follows its target's release line. A redelivery
// that asks nothing new re-converges the consent: the installation stays
// active and healthy, its approved generation advances, every grant is kept.
// A redelivery that asks more leaves it on its consent, healthy, offering an
// upgrade, and nothing changes until an administrator reviews and installs
// the delivered release on the same installation; grants are kept then too.
// The host compares no generation of its own: health reads the core's consent.
func TestRedeliveryKeepsSolutionEntitled(t *testing.T) {
	alias := uniqueAlias("example-release-line")
	binding := "acme.test." + alias
	target := declarePresence(t, binding, alias)
	owner := seedUser(t)
	seedHumanPrincipal(t, owner, "Jane Doe")
	org := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, owner, "admin"))
	store := testStore.AuthorityCoreWithConsent(nil)
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(store, authority.Config{Issuer: "https://authority.example.com", KeyID: "test", Signer: private,
		Keys: canonicalFixtureKeys{"test": public}, Replay: workcontext.NewMemoryReplayStore(), HostInstallsModules: true})
	require.NoError(t, err)
	caller := authority.Person{ID: owner, Organization: org}

	page, draft := alias+".page", alias+".draft"
	read := func(kinds ...string) []*basev0.WorkScopeV1 {
		var out []*basev0.WorkScopeV1
		for _, kind := range kinds {
			out = append(out, &basev0.WorkScopeV1{ResourceKind: kind, Actions: []string{"read"}})
		}
		return out
	}
	all := []*basev0.WorkScopeV1{{ResourceKind: page, Actions: []string{"read", "write"}}, {ResourceKind: draft, Actions: []string{"read", "write"}}}
	// deliver is the delivery of generation n, on both sides of the host: the
	// core's declaration and the host's applied binding.
	deliver := func(generation uint64, roles map[string][]*basev0.WorkScopeV1) {
		t.Helper()
		roles[domain.InstallerRole] = all
		m := domain.Module{Name: alias, Principal: "solution:" + alias, Kind: domain.KindSolution, Generation: generation,
			ResourceKinds: all, InstallationRole: domain.NoInstallationRole, Roles: roles, OwnerAware: true}
		_, err := core.ApplyModule(testCtx, m)
		require.NoError(t, err)
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.SaveSolutionHostBinding(ctx, appliedPresenceRecordKind(t, binding, alias, generation, time.Now().UTC(), false, "solution"))
		}))
	}
	// plan is the host's review of the delivered release, as installReviewedSolution carries it.
	plan := func(approval domain.ConsentApproval) context.Context {
		p := business.SolutionInstallationPlan{Organization: org, Target: alias, MachineRole: domain.NoInstallationRole, Approval: approval, HostTargetID: target, BindingID: binding}
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return storetx.Tx(ctx).QueryRow(ctx, `SELECT applied_release,applied_digest FROM public.solution_host_bindings WHERE binding_id=$1`, binding).Scan(&p.Release, &p.DeliveryDigest)
		}))
		return business.WithSolutionInstallationPlan(testCtx, p)
	}
	summary := func() *gen.InstallationSummary {
		t.Helper()
		rows, err := testStore.CanonicalSolutionSummaries(testCtx, org, "", "", gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0]
	}
	stands := func(approved uint64, upgrade bool) *gen.Installation {
		t.Helper()
		s := summary()
		require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, s.Installation.Status)
		require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, s.Health, "the installation is healthy at every step of its release line")
		require.Equal(t, approved, s.Installation.ApprovedGeneration)
		require.Equal(t, upgrade, s.Installation.UpgradeAvailable)
		return s.Installation
	}
	grants := func(id string) int {
		t.Helper()
		var n int
		require.NoError(t, store.Read(testCtx, func(tx authority.Tx) error {
			out, err := tx.RoleGrants(id)
			n = len(out)
			return err
		}))
		return n
	}
	approved := func(generation uint64) {
		t.Helper()
		require.NoError(t, store.Read(testCtx, func(tx authority.Tx) error {
			m, err := tx.ApprovedModule(alias, generation)
			require.Equal(t, generation, m.Generation, "the approved declaration is kept per generation")
			return err
		}))
	}

	deliver(1, map[string][]*basev0.WorkScopeV1{"viewer": read(page, draft)})
	approval, err := core.Review(testCtx, caller, org, alias)
	require.NoError(t, err)
	approval.OperationID = business.NewIDString()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	inst, err := core.Install(plan(approval), caller, org, alias, "", "", &approval, func(ctx context.Context, pending domain.Installation) error {
		jobID, err := testStore.EnqueueCanonicalInstallationStep(ctx, pending, false)
		if err != nil {
			return err
		}
		job := claimInstallationFor(t, worker, pending.ID)
		require.Equal(t, jobID, job.Id)
		return worker.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: installationLease(job)})
	})
	require.NoError(t, err)
	require.Equal(t, domain.Active, inst.Status)
	_, err = core.GrantRole(testCtx, caller, authority.RoleRequest{Organization: org, InstallationID: inst.ID, Role: "viewer", PersonID: owner})
	require.NoError(t, err)
	installed := stands(1, false)
	require.Equal(t, 1, grants(inst.ID))
	approved(1)

	t.Run("a redelivery asking nothing new re-converges the consent and keeps the grants", func(t *testing.T) {
		deliver(2, map[string][]*basev0.WorkScopeV1{"viewer": read(page)})
		current := stands(2, false)
		require.Greater(t, current.Revision, installed.Revision, "the revision moves with the re-convergence")
		require.Equal(t, 1, grants(inst.ID))
		approved(2)
	})
	t.Run("a redelivery asking more leaves the installation healthy on its consent, offering an upgrade", func(t *testing.T) {
		deliver(3, map[string][]*basev0.WorkScopeV1{"viewer": read(page, draft), "editor": {{ResourceKind: page, Actions: []string{"read", "write"}}}})
		stands(2, true)
		require.Equal(t, 1, grants(inst.ID))
	})
	t.Run("a fresh review and install upgrade it in place, grants kept", func(t *testing.T) {
		approval, err := core.Review(testCtx, caller, org, alias)
		require.NoError(t, err)
		approval.OperationID = business.NewIDString()
		// Production converges the reviewed closure here; this solution reaches nothing.
		upgraded, err := core.Install(plan(approval), caller, org, alias, "", "", &approval, func(context.Context, domain.Installation) error { return nil })
		require.NoError(t, err)
		require.Equal(t, inst.ID, upgraded.ID)
		stands(3, false)
		require.Equal(t, 1, grants(inst.ID))
		approved(3)
	})
}
