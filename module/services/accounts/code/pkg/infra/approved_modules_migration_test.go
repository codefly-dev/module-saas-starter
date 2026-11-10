//go:build !pure

package infra_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// approvedModulesFill is migration 68's fill of the approved declarations, as
// the migration states it.
func approvedModulesFill(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob("../../../../store/migrations/68_*.up.sql")
	require.NoError(t, err)
	require.Len(t, paths, 1, "migration 68")
	body, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	fill := string(body)
	at := strings.Index(fill, "INSERT INTO public.authority_core_approved_modules")
	require.GreaterOrEqual(t, at, 0, "the migration fills the approved declarations")
	return fill[at:]
}

// Migration 68 writes a consent's approved declaration only where the
// generation it approved is the module's current one, which is the approved
// declaration only then; a consent that approved an older generation gets
// nothing, at that generation or the current one: the current declaration
// was never approved for it. Applying it again changes nothing.
func TestMigration68FillsOnlyTheDeclarationACurrentConsentApproved(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	current, behind := uniqueAlias("approved-current"), uniqueAlias("approved-behind")
	module := func(name string, generation uint64) domain.Module {
		return domain.Module{Name: name, Principal: "solution:" + name, Kind: domain.KindSolution, Generation: generation,
			Roles: map[string][]*basev0.WorkScopeV1{domain.InstallerRole: {{ResourceKind: name + ".page", Actions: []string{"read", "write"}}}}}
	}
	consent := func(name string, generation uint64) domain.Installation {
		return domain.Installation{ID: business.NewIDString(), Organization: org, Target: name, Status: domain.Active, Revision: 1,
			AgentRole: domain.NoInstallationRole, PrincipalID: business.NewIDString(), InstalledBy: owner,
			Consent: domain.ConsentReceipt{OperationID: business.NewIDString(), Intent: "install", Revision: 1, Generation: generation, Digest: "sha256:" + name}}
	}
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
		for _, m := range []domain.Module{module(current, 2), module(behind, 3)} {
			if err := tx.PutModule(m); err != nil {
				return err
			}
		}
		for _, inst := range []domain.Installation{consent(current, 2), consent(behind, 1)} {
			if err := tx.PutInstallation(inst); err != nil {
				return err
			}
		}
		return nil
	}))

	fill := approvedModulesFill(t)
	for range 2 {
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, fill)
			return err
		}))
	}

	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		kept, err := tx.ApprovedModule(current, 2)
		require.NoError(t, err, "a consent at the current generation is filled")
		composed, err := tx.ModuleNamed(current)
		require.NoError(t, err)
		require.Equal(t, composed, kept, "with the module's current declaration")
		for _, generation := range []uint64{1, 3} {
			_, err = tx.ApprovedModule(behind, generation)
			require.ErrorIs(t, err, domain.ErrNotFound, "a consent behind the current generation is filled at no generation")
		}
		return nil
	}))
}
