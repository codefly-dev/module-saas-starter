//go:build !pure

package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
)

// Migration 23's two triggers, exercised.
//
// `installations.target_id` replaced a free-text `solution_identifier`, and the
// invariants that make the target an IDENTITY rather than a label are enforced in
// the DATABASE rather than in each writer. Until these tests existed, both
// triggers were present and unexercised — the column was written and audited, so
// every passing test was consistent with the triggers never firing at all.
//
// Enforced in the database on purpose: a check in the install path binds that
// path only, and an installation row is also written by migrations, by the
// reconciler's revoke pass and by any future admin tool. "No writer forgets" is
// a promise a trigger keeps and a convention does not.

// A target cannot be repointed once set.
//
// This is what makes the target the installation's identity: if it could move,
// the row would be a mutable pointer and "which solution is this installation
// for" would have no durable answer — a consent record that can be reassigned is
// not a consent record.
func TestInstallationTargetCannotBeRepointed(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "repoint@example.com", "repoint", "Repoint Co")
	roleID := seedInstallableRole(t, ctx, orgID)

	targetID, _ := declarePresence(t, "first-solution")
	otherTargetID, _ := declarePresence(t, "second-solution")

	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/first:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "First Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.NoError(t, err)

	// Repointing at ANOTHER live target is refused. The other target is live, so
	// nothing here is explained by the liveness trigger below.
	err = testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		_, execErr := storetx.Tx(ctx).Exec(ctx,
			`UPDATE public.installations SET target_id = $1 WHERE id = $2::uuid`,
			otherTargetID, installation.Id)
		return execErr
	})
	require.Error(t, err, "an installation's target must not be repointable")
	require.Contains(t, err.Error(), "immutable",
		"the refusal must name the invariant, so a writer learns why rather than that an UPDATE failed")

	// Clearing it is refused too, which is the quieter half: a NULL target on an
	// active row would satisfy "not repointed" while erasing what the
	// installation consents to.
	err = testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		_, execErr := storetx.Tx(ctx).Exec(ctx,
			`UPDATE public.installations SET target_id = NULL WHERE id = $1::uuid`, installation.Id)
		return execErr
	})
	require.Error(t, err, "an installation's target must not be clearable")

	// And the control: an unrelated column on the same row still updates, so the
	// refusals above are the trigger and not a privilege or locking failure that
	// would block every write.
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		_, execErr := storetx.Tx(ctx).Exec(ctx,
			`UPDATE public.installations SET co_owner_principal_ids = $1 WHERE id = $2::uuid`,
			[]string{adminID}, installation.Id)
		return execErr
	}))

	// The application cannot REMOVE the invariant either, and that is the
	// property that makes enforcing it in the database worth more than
	// enforcing it in the install path.
	//
	// I found this by trying to mutate the trigger away to prove it was what
	// refused: the control-plane role is not the table's owner, so PostgreSQL
	// answers `must be owner of relation installations`. So the enforcement sits
	// BENEATH the privilege level every application path runs at — a compromise
	// of this service cannot lift it, and neither can a careless migration run
	// as the app role.
	err = testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		_, execErr := storetx.Tx(ctx).Exec(ctx,
			`DROP TRIGGER installations_target_immutable ON public.installations`)
		return execErr
	})
	require.Error(t, err, "the application role must not be able to drop the invariant it is bound by")
	require.Contains(t, err.Error(), "must be owner",
		"the refusal must be ownership, not a missing trigger — a DROP that succeeded quietly would leave the suite green and the invariant gone")
}

// An active installation cannot name a withdrawn target.
//
// Presence is withdrawn by a tombstone generation that closes the target. An
// installation left active against a closed target would be consent to something
// the host no longer serves, and the next capability minted for it would name a
// solution that is gone.
func TestActiveInstallationCannotNameAWithdrawnTarget(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "withdrawn@example.com", "withdrawn", "Withdrawn Co")
	roleID := seedInstallableRole(t, ctx, orgID)

	targetID, alias := declarePresence(t, "withdrawn-solution")

	// Close the target, as a tombstone generation does.
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.CloseSolutionTarget(ctx, targetID, 2, time.Now().UTC())
	}))

	_, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/withdrawn:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "Withdrawn Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.Error(t, err, "installing against a withdrawn target must be refused")

	// A target that never existed is refused too, and distinctly: an unknown id
	// is not the same fact as a withdrawn one, and a trigger that reported them
	// alike would hide a typo as a withdrawal.
	_, err = testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/ghost:1.0.0",
		TargetID:        business.NewIDString(),
		RootScopeLabel:  "Ghost Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.Error(t, err, "installing against an unknown target must be refused")

	// The control: a LIVE target installs, so the refusals above are liveness and
	// not the install path being broken for every input.
	liveTargetID, _ := declarePresence(t, "live-solution")
	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/live:1.0.0",
		TargetID:        liveTargetID,
		RootScopeLabel:  "Live Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, installation.Id)
	require.NotEqual(t, alias, "", "the withdrawn alias is referenced so the fixture is not silently unused")
}

// seedInstallableRole creates the contributed role an install needs.
func seedInstallableRole(t *testing.T, ctx context.Context, orgID string) string {
	t.Helper()
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "installable role " + roleID,
			Description: "least-privilege contributed role",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "write"}},
		})
	}))
	return roleID
}
