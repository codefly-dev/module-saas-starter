//go:build !pure

package business_test

import (
	"context"
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"accounts/fixtures"
	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
)

// A fresh local run: an empty store, the one fixture every local run seeds
// (dev-admin, with its row standing for every target), a host deciding through
// its authority core, and a composition delivered as the CLI delivers it at
// boot — each solution once, then each again at its next generation while its
// install is still waiting on its setup. The fixture runs after every delivery
// pass, as work.go wires it, through production's install step: the setup job
// is answered between passes, as the solution's own setup answers it. Every
// solution ends active, installed by Alice, and Bob, Carol and Dana each sit on
// the team of their role, which holds that role on the installation.
func TestAFreshRunsFixtureInstallsEverySolutionActiveAndSeatsEachPersonOnTheirRole(t *testing.T) {
	clearData(t)
	ctx := testCtx
	t.Setenv("DEV_FIXTURE_PATH", "") // the module's own dev-admin, as a local run selects it
	signer := wireSignedDelivery(t)
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, testService.ConfigureAuthorityCore(ctx, "https://accounts.example.test", "fresh-run", key))
	t.Cleanup(testService.UnconfigureAuthorityCoreForTest)
	answerSetups := setupAnswerer(t)

	installations, err := fixtures.SeedWithInstallations(ctx, testService, "dev-admin")
	require.NoError(t, err)
	legacyReconciler.AfterEachPass(installations.Converge)
	t.Cleanup(func() { legacyReconciler.AfterEachPass(nil) })

	solutions := []string{uniqueAlias("fresh-run-one"), uniqueAlias("fresh-run-two")}
	for _, alias := range solutions {
		deliverRoleGatedSolution(t, signer, alias, 1)
	}
	// The boot's second round arrives while each install waits on its setup:
	// each fresh review supersedes the attempt the first round started.
	for _, alias := range solutions {
		deliverRoleGatedSolution(t, signer, alias, 2)
	}
	for range 2 { // the setups answer; the next pass activates what they answered
		answerSetups()
		require.NoError(t, legacyReconcile(ctx))
	}

	alice := seededUUIDFor(t, ctx, "dev-alice")
	acme := seededOrgIDFor(t, ctx, alice, "Acme Corp")
	caller := authority.Person{ID: alice, Organization: acme}
	core, err := testService.AuthorityCore()
	require.NoError(t, err)
	held, err := core.Installations(ctx, caller, acme)
	require.NoError(t, err)
	people := map[string]string{"viewer": "dev-bob", "editor": "dev-carol", "installer": "dev-dana"}
	for _, alias := range solutions {
		i := slices.IndexFunc(held, func(inst domain.Installation) bool { return inst.Target == alias })
		require.GreaterOrEqual(t, i, 0, "%s is installed at Acme Corp", alias)
		inst := held[i]
		require.Equal(t, domain.Active, inst.Status, "%s is active", alias)
		require.Equal(t, uint64(2), inst.Consent.Generation, "%s approves the generation delivered last", alias)
		require.Equal(t, alice, inst.InstalledBy)
		for role, person := range people {
			holding, err := testService.InstallationRoleTeams(ctx, caller, acme, inst.ID, role)
			require.NoError(t, err)
			require.NotEmpty(t, holding, "a team holds %s on %s", role, alias)
			joined, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: acme, MemberId: seededUUIDFor(t, ctx, person)})
			require.NoError(t, err)
			require.True(t, slices.ContainsFunc(joined.GetTeams(), func(team *gen.Team) bool { return slices.Contains(holding, team.GetId()) }),
				"%s sits on the team holding %s on %s", person, role, alias)
		}
	}
	require.NoError(t, legacyReconcile(ctx), "a converged fixture changes nothing on a later pass")
}

// deliverRoleGatedSolution delivers alias at generation through the
// composition interface with the authority document a solution declares: an
// installer, an editor and a viewer role over what it serves, and no standing
// authority of its own.
func deliverRoleGatedSolution(t *testing.T, signer *solutionhost.InMemorySigner, alias string, generation uint64) {
	t.Helper()
	m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: alias, Alias: alias, Kind: solutionhost.KindSolution, Generation: generation,
		Host: solutionhost.HostTarget{Coordinate: legacyDeliveryCoordinate, Domain: "acme"}, Release: solutionhost.Release{Publisher: "acme", Name: alias, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "app"}},
		Authority: &solutionhost.Authority{Principal: alias, ResourceKinds: []string{"doc"}, InstallationRole: domain.NoInstallationRole}}
	raw, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	read := []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read"}}}
	write := []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read", "write"}}}
	doc, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: alias, Generation: generation, CoreManifestDigest: delivery.Digest(raw),
		InstallationRole: domain.NoInstallationRole, OwnerAware: true, ScopeCeilings: write,
		Roles: []*compositionv1.RoleV1{{Name: business.InstallerRole, Scopes: write}, {Name: "editor", Scopes: write}, {Name: "viewer", Scopes: read}}})
	require.NoError(t, err)
	envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
	require.NoError(t, err)
	_, err = testService.ApplyComposition(testCtx, &compositionv1.ApplyManifestRequest{
		Carrier:    &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())},
		Authority:  &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope},
		Credential: "delivery:" + alias,
	})
	require.NoError(t, err, "deliver %s at generation %d", alias, generation)
}

// setupAnswerer gives the shared service the host's setup queue for the length
// of one test, and answers every installation setup waiting on it, as each
// solution's own setup acknowledges its step.
func setupAnswerer(t *testing.T) func() {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	worker := infra.NewPostgresJobStore(pool)
	testService.SetJobOperations(worker)
	t.Cleanup(func() { testService.SetJobOperations(nil) })
	return func() {
		t.Helper()
		claimed, err := worker.Claim(context.WithoutCancel(testCtx), &jobsv1.ClaimJobsRequest{Queue: business.InstallationStepQueue, WorkerId: "fresh-run-setup", Limit: 100,
			LeaseDuration: durationpb.New(business.InstallationStepLease)})
		require.NoError(t, err)
		for _, job := range claimed.GetJobs() {
			require.NoError(t, worker.Complete(testCtx, &jobsv1.CompleteJobRequest{Lease: &jobsv1.JobLeaseReference{JobId: job.GetId(), WorkerId: "fresh-run-setup", LeaseToken: job.GetLease().GetToken()}}))
		}
	}
}
