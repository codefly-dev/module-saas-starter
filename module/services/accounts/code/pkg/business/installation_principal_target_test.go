//go:build pure

package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallDependencyPrincipalResolvesThroughHostCatalogue(t *testing.T) {
	const principal = "module:example-records"
	target := &AvailableSolutionTarget{TargetID: "550e8400-e29b-41d4-a716-446655440000", BindingID: "example-binding", RouteAlias: "records-route", Kind: "module", AppliedGeneration: 7, ReleasePublisher: "acme.example", ReleaseName: "records", ReleaseVersion: "1.0.0"}
	s := &Service{store: availableInstallationStore{targets: []*AvailableSolutionTarget{target}}}
	s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(principal): {Prefix: principal, PresenceBinding: target.BindingID, PresenceGeneration: 7}})
	actual, err := s.ResolveInstallationPrincipal(context.Background(), "org", principal)
	require.NoError(t, err)
	require.Equal(t, target, actual)
	require.NotEqual(t, principal, actual.TargetID)
	for _, nearMiss := range []string{target.RouteAlias, target.BindingID, target.TargetID, "acme.example/records:1.0.0", "example-records"} {
		t.Run(nearMiss, func(t *testing.T) {
			_, err := s.ResolveInstallationPrincipal(context.Background(), "org", nearMiss)
			require.ErrorContains(t, err, nearMiss)
		})
	}
	unrelated := &AvailableSolutionTarget{TargetID: "another-target", BindingID: "another-binding", Kind: "module", AppliedGeneration: 7}
	s.store = availableInstallationStore{targets: []*AvailableSolutionTarget{unrelated, target}}
	actual, err = s.ResolveInstallationPrincipal(context.Background(), "org", principal)
	require.NoError(t, err)
	require.Equal(t, target, actual, "an unrelated catalogue row cannot shadow the exact binding")
	s.store = availableInstallationStore{targets: []*AvailableSolutionTarget{target, target}}
	_, err = s.ResolveInstallationPrincipal(context.Background(), "org", principal)
	require.ErrorContains(t, err, principal, "ambiguous accepted targets refuse")
	s.store = availableInstallationStore{targets: []*AvailableSolutionTarget{unrelated}}
	_, err = s.ResolveInstallationPrincipal(context.Background(), "org", principal)
	require.ErrorContains(t, err, principal, "missing exact catalogue target refuses")
	s.store = availableInstallationStore{targets: []*AvailableSolutionTarget{target}}
	target.AppliedGeneration++
	_, err = s.ResolveInstallationPrincipal(context.Background(), "org", principal)
	require.ErrorContains(t, err, "INSTALLATION_DELIVERY_CHANGED")
	target.AppliedGeneration--
	s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(principal): {Prefix: principal, PresenceBinding: target.BindingID, PresenceGeneration: 7, Tenant: "other"}})
	_, err = s.ResolveInstallationPrincipal(context.Background(), "org", principal)
	require.ErrorContains(t, err, principal)
}
