//go:build !pure

package business_test

import (
	"context"
	"errors"

	"fmt"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stretchr/testify/require"

	"accounts/fixtures"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
)

// A fixture's declared installation is performed through the host's own
// install path, as its owner, once the composition has delivered its target:
// before that it waits, and once held it is reused, by this process and by a
// reseed, so a fresh local run comes up installed without anyone installing by
// hand.
func TestAFixtureInstallsWhatItDeclaresOnceItIsDelivered(t *testing.T) {
	for _, kind := range []business.SolutionDeclaredKind{business.SolutionDeclaredKindSolution, business.SolutionDeclaredKindModule} {
		t.Run(string(kind), func(t *testing.T) {
			clearData(t)
			ctx := testCtx
			alias := uniqueAlias("fixture-installed")
			fixturePath := filepath.Join(t.TempDir(), "installs.yaml")
			t.Setenv("DEV_FIXTURE_PATH", fixturePath)
			require.NoError(t, os.WriteFile(fixturePath, []byte(fmt.Sprintf(`users:
  - email: owner@install.test
    provider: email
    provider_id: fixture-install-owner
  - email: member@install.test
    provider: email
    provider_id: fixture-install-member
organizations:
  - name: Installing Org
    owner: owner@install.test
    members:
      - email: member@install.test
        role: member
roles:
  - org: Installing Org
    name: Solution reader
    permissions:
      - {resource: doc, action: read}
installations:
  - org: Installing Org
    target: %s
    role: Solution reader
    owner: owner@install.test
`, alias)), 0o600))

			installations, err := fixtures.SeedWithInstallations(ctx, testService, "installs")
			require.NoError(t, err)
			require.NotNil(t, installations)
			owner := seededUUIDFor(t, ctx, "fixture-install-owner")
			orgID := seededOrgIDFor(t, ctx, owner, "Installing Org")

			require.NoError(t, installations.Converge(ctx), "an undelivered target is waited on, not refused")
			require.Empty(t, organizationInstallations(t, orgID))

			targetID := declarePresenceAt(t, alias, kind)
			require.NoError(t, installations.Converge(ctx))
			held := organizationInstallations(t, orgID)
			require.Len(t, held, 1)
			require.Equal(t, targetID, held[0].GetTargetId())
			require.Equal(t, owner, held[0].GetOwnerPrincipalId(), "the declared owner installs and owns it")
			if kind == business.SolutionDeclaredKindSolution {
				require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, held[0].GetStatus())
			} else {
				require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, held[0].GetStatus())
				caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID(alias), BoundOrg: orgID}
				_, err := testService.AuthorizeModuleCapability(ctx, caller)
				require.NoError(t, err, "the host's fixture consent makes a new installation usable")
				// A local database from before snapshots were required is repaired by
				// the host reapplying its own declared setup on the next seed.
				require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
					_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE public.installations SET scope='{"capabilities":true,"follows":true}'::jsonb WHERE id=$1`, held[0].Id)
					return err
				}))
				_, err = testService.AuthorizeModuleCapability(ctx, caller)
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
			}

			require.NoError(t, installations.Converge(ctx))
			again, err := fixtures.SeedWithInstallations(ctx, testService, "installs")
			require.NoError(t, err)
			require.NoError(t, again.Converge(ctx), "a reseed finds the installation it declared, pending or not")
			require.Len(t, organizationInstallations(t, orgID), 1)
			if kind == business.SolutionDeclaredKindModule {
				_, err := testService.AuthorizeModuleCapability(ctx, business.ModuleCaller{PrincipalID: business.ModulePrincipalID(alias), BoundOrg: orgID})
				require.NoError(t, err, "fixture convergence re-consents an existing following installation")
			}
		})
	}
}

// The resolver names each refusal: a target the catalogue does not offer, and a
// role the organisation does not have.
func TestInstallTargetByNameNamesWhatItCannotResolve(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "byname@example.com", "byname", "By Name Co")
	_, alias := declarePresence(t, "by-name")

	_, _, err := testService.InstallTargetByName(ctx, adminID, orgID, "nothing-delivered", "viewer", "")
	require.True(t, errors.Is(err, business.ErrInstallTargetNotPresent), "got %v", err)
	require.ErrorContains(t, err, `"nothing-delivered"`)

	_, _, err = testService.InstallTargetByName(ctx, adminID, orgID, alias, "no such role", "")
	require.ErrorContains(t, err, `has no role named "no such role"`)
}

func organizationInstallations(t *testing.T, orgID string) []*gen.Installation {
	t.Helper()
	resp, err := testService.ListInstallations(testCtx, &gen.ListInstallationsRequest{OrgId: orgID})
	require.NoError(t, err)
	var out []*gen.Installation
	for _, summary := range resp.GetInstallations() {
		out = append(out, summary.GetInstallation())
	}
	return out
}
