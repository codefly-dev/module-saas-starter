//go:build !pure

package business_test

import (
	"crypto/ed25519"
	"testing"

	"accounts/pkg/business"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// A module installed by name before it declared roles is a host that does not
// start: its target is role-gated now, so its entitlement would be read from
// the core's record alone and silently lost. Boot names the organization, the
// target and the installation, and the remedy; once the installation is
// uninstalled the host starts.
func TestBootRefusesAModulePageInstalledByName(t *testing.T) {
	clearData(t)
	admin, org := mustUserAndOrg(t, testCtx, "admin@page-by-name.test", "page-by-name", "Page By Name")
	installation, _, alias := installForExposure(t, admin, org)
	page := func(generation uint64, roles map[string][]*basev0.WorkScopeV1) {
		t.Helper()
		require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
			return tx.PutModule(domain.Module{Name: alias, Principal: alias, Kind: domain.KindModule, Generation: generation, Roles: roles})
		}))
	}
	page(2, map[string][]*basev0.WorkScopeV1{"viewer": {{ResourceKind: alias + ".note", Actions: []string{"read"}}}})
	t.Cleanup(func() { page(3, nil) })
	boot := func() error {
		svc, err := business.NewService(testStore)
		require.NoError(t, err)
		_, key, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		return svc.ConfigureAuthorityCore(testCtx, "https://accounts.example.test", "page-by-name", key)
	}

	err := boot()
	require.ErrorIs(t, err, business.ErrUnreviewedModulePage)
	require.ErrorContains(t, err, "organization "+org+", target "+alias+", installation "+installation)
	require.ErrorContains(t, err, "uninstall each one, then install its target through its review")

	require.NoError(t, testService.UninstallSolution(testCtx, admin, org, installation))
	require.NotErrorIs(t, boot(), business.ErrUnreviewedModulePage)
}
