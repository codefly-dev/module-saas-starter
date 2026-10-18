package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The cell's namespace rule is read off this host's own placement, both
// spellings, and a placement no rule explains refuses the boot by name.
func TestTheModuleNamespaceRuleIsReadOffTheHostsOwnPlacement(t *testing.T) {
	bare, err := NewModuleNamespaceRule("saas", "saas")
	require.NoError(t, err)
	require.Equal(t, "tracing", bare.Namespace("tracing"), "core's rule: the namespace named after the module")

	prefixed, err := NewModuleNamespaceRule("platform-acme-staging-saas", "saas")
	require.NoError(t, err)
	require.Equal(t, "platform-acme-staging-tracing", prefixed.Namespace("tracing"),
		"a cell composing several modules prefixes the module with its environment's namespace")

	for _, placement := range [][2]string{
		{"platform-acme-staging", "saas"},
		{"saas-platform", "saas"},
		{"-saas", "saas"},
		{"saas", ""},
		{"", "saas"},
	} {
		_, err := NewModuleNamespaceRule(placement[0], placement[1])
		require.Error(t, err, "namespace %q as module %q", placement[0], placement[1])
		if placement[0] != "" && placement[1] != "" {
			require.ErrorContains(t, err, placement[0])
			require.ErrorContains(t, err, placement[1])
		}
	}
}
