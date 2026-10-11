//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
)

// HOST-INST-002, HOST-INST-004, HOST-INST-006: every enforcement-at-use
// installation reader withholds pending/failed consent, including the worker's
// SQL predicate and lock, the gateway projection, grants, and ordinary mints.
func TestPendingInstallationEnforcementReaders(t *testing.T) {
	for _, state := range []string{"pending", "failed"} {
		t.Run(state, func(t *testing.T) {
			i := newDynamicInstall(t, "", "", "")
			team := seedTeamWith(t, i.org, i.owner)
			grantAtNode(t, i.org, team, solutionRole(t, i.org), i.installation)
			require.Equal(t, []string{i.target}, entitledIdentifiers(t, i.org, i.owner))
			_, err := testStore.ResolveInstallationAuthority(testCtx, i.org, i.installation.Id, nil)
			require.NoError(t, err, "positive control: ordinary mint resolves active consent")
			require.NoError(t, testStore.WithOrgTx(testCtx, i.org, func(ctx context.Context) error {
				_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE installations SET status=$2,
				 failure_reason=CASE WHEN $2='failed' THEN 'Example refusal' ELSE NULL END WHERE id=$1`, i.installation.Id, state)
				return err
			}))
			_, err = testStore.LiveModuleAuthority(testCtx, i.principal, i.org, i.installation.Id)
			require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
			_, err = testStore.ResolveInstallationAuthority(testCtx, i.org, i.installation.Id, nil)
			require.Error(t, err, "ordinary StartInstallationTask must refuse")
			require.NoError(t, testStore.WithOrgTx(testCtx, i.org, func(ctx context.Context) error {
				_, err := testStore.InstallationAuthorityState(ctx, i.org, i.installation.Id)
				require.ErrorContains(t, err, business.ErrModuleInstallationInactive.Error())
				_, err = testStore.InstallationRootScopePath(ctx, i.org, i.installation.Id)
				require.Error(t, err, "team grants require an active root")
				exposure, err := testStore.ListInstallationExposure(ctx, i.org, []string{i.installation.Id})
				require.NoError(t, err)
				require.Empty(t, exposure)
				entitlements, err := testStore.ListSolutionEntitlements(ctx, i.org, i.owner, "", 100)
				require.NoError(t, err)
				require.Empty(t, entitlements, "gateway projection must omit unacknowledged consent")
				stamp, err := json.Marshal(jobs.ModuleAuthority{PrincipalID: i.principal, OrganizationID: i.org,
					InstallationID: i.installation.Id, InstallationRevision: 1, BindingID: i.binding}.Attributes())
				if err != nil {
					return err
				}
				var allowed bool
				err = storetx.Tx(ctx).QueryRow(ctx, `SELECT public.module_job_authority_current($1::jsonb)`, stamp).Scan(&allowed)
				require.NoError(t, err)
				require.False(t, allowed)
				return nil
			}))
			err = testStore.WithOrgTx(testCtx, i.org, func(ctx context.Context) error {
				stamp, err := json.Marshal(jobs.ModuleAuthority{PrincipalID: i.principal, OrganizationID: i.org,
					InstallationID: i.installation.Id, InstallationRevision: 1, BindingID: i.binding}.Attributes())
				if err != nil {
					return err
				}
				_, err = storetx.Tx(ctx).Exec(ctx, `SELECT public.lock_module_job_authority($1::jsonb)`, stamp)
				return err
			})
			require.ErrorContains(t, err, "revoked_authority")
		})
	}
}
