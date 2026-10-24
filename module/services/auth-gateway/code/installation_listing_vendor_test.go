package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVendoredInstallationListingMatchesAccounts holds every definition in the
// gateway's installation_listing.proto byte-identical to the one accounts
// declares in installations.proto. The gateway carries only the messages
// ListInstallations takes and answers, because installations.proto imports
// the policy options and google.api this service does not vendor; a copy that
// drifted would decode a different wire shape from the one accounts writes,
// silently.
func TestVendoredInstallationListingMatchesAccounts(t *testing.T) {
	vendored, err := os.ReadFile(filepath.Join("..", "proto", "saas", "accounts", "v1", "installation_listing.proto"))
	require.NoError(t, err)
	owner, err := os.ReadFile(filepath.Join("..", "..", "accounts", "proto", "saas", "accounts", "v1", "installations.proto"))
	require.NoError(t, err)

	definitions := regexp.MustCompile(`(?ms)^(?:message|enum) \w+ \{\n.*?^\}\n`).FindAllString(string(vendored), -1)
	require.Len(t, definitions, 6, "the vendored file declares the two enums and four messages ListInstallations uses")
	for _, definition := range definitions {
		require.True(t, strings.Contains(string(owner), definition),
			"installation_listing.proto drifted from accounts' installations.proto:\n%s", definition)
	}
}
