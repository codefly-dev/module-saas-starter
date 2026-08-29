package orgsettings_test

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/orgsettings"

	"github.com/stretchr/testify/require"
)

func TestResolveClonesAndNeverReturnsNil(t *testing.T) {
	resolved, err := orgsettings.Resolve(nil)
	require.NoError(t, err)
	require.NotNil(t, resolved)

	stored := &gen.OrganizationSettings{}
	resolved, err = orgsettings.Resolve(stored)
	require.NoError(t, err)
	require.NotSame(t, stored, resolved, "Resolve must return a clone")
}

func TestJSONCodecRoundTripsEmptyDocument(t *testing.T) {
	encoded, err := orgsettings.JSON.Marshal(&gen.OrganizationSettings{})
	require.NoError(t, err)
	require.JSONEq(t, "{}", string(encoded))

	decoded, err := orgsettings.JSON.Unmarshal(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded)
}

func TestValidateResetPathsRejectsUnsupportedPaths(t *testing.T) {
	patch := &gen.OrganizationSettings{}

	// No common org settings exist, so a bare (non-composed) path is unknown.
	err := orgsettings.ValidateResetPaths(patch, []string{"appearance.theme"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")

	// A composed path with no matching contributed field is likewise unknown
	// (the org field catalog is empty until a scope: org contribution lands).
	err = orgsettings.ValidateResetPaths(patch, []string{"composed.unknown_field"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}

func TestValidateResetPathsAllowsEmpty(t *testing.T) {
	require.NoError(t, orgsettings.ValidateResetPaths(&gen.OrganizationSettings{}, nil))
	require.NoError(t, orgsettings.ValidateResetPaths(nil, []string{}))
}

func TestApplyResetsRejectsUnsupportedPath(t *testing.T) {
	err := orgsettings.ApplyResets(&gen.OrganizationSettings{}, []string{"composed.unknown_field"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}
