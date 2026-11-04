//go:build pure

package business

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

func TestModuleNoStandingSelectionRequiresCurrentPairedDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, role string
		stale, denied, none     bool
	}{
		{"declared none", "none", "", false, false, true},
		{"caller role refused", "none", "role-id", false, true, false},
		{"caller literal none refused", "none", "none", false, true, false},
		{"old generation", "none", "", true, true, false},
		{"legacy stays explicit", "", "role-id", false, false, false},
		{"legacy without a role refused", "", "", false, true, false},
		{"named module cannot become catalogue role", "reader", "catalogue-role-id", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := memstore.New()
			m := domain.Module{Name: "records-route", Principal: "records-principal", Kind: domain.KindModule, Generation: 2, InstallationRole: tc.declaration}
			if tc.stale {
				m.Generation = 1
			}
			require.NoError(t, store.Write(ctx, func(tx authority.Tx) error { return tx.PutModule(m) }))
			public, private, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			core, err := authority.New(store, authority.Config{Issuer: "test.example", KeyID: "key", Signer: private, Keys: staticKeys{"key": public}, Replay: workcontext.NewMemoryReplayStore()})
			require.NoError(t, err)
			s := &Service{store: availableInstallationStore{targets: []*AvailableSolutionTarget{{TargetID: "host-target", BindingID: "host-binding", RouteAlias: m.Name, Kind: "module", AppliedGeneration: 2}}}, authorityCore: &authorityCoreState{core: core}}
			s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(m.Principal): {Prefix: m.Principal, PresenceBinding: "host-binding", PresenceGeneration: 2}})
			params := &InstallSolutionParams{OrgID: "org", TargetID: "host-target", RoleID: tc.role, NoStandingAuthority: true}
			_, err = s.prepareInstallation(ctx, params)
			if tc.denied {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.none, params.NoStandingAuthority, "caller-supplied marker is discarded; only the declaration selects it")
			require.True(t, params.Scope.Capabilities, "no standing agent role does not erase module consent")
		})
	}
}
