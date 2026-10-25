package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// No binding may be addressed to the module capability surface: a context
// addressed there is read as a module identity, so a binding naming it would
// let a hop or a headless mint spell one.
func TestABindingAddressedToTheModuleCapabilitySurfaceIsRefused(t *testing.T) {
	invoke := []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}
	for name, binding := range map[string]ModuleOperationAudience{
		"invoke":   {Audience: ModuleCapabilitiesAudience, InvokeScopes: invoke},
		"headless": {Audience: ModuleCapabilitiesAudience, HeadlessScopes: invoke},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateOperationAudiences("example", map[string]ModuleOperationAudience{"surface": binding})
			require.ErrorContains(t, err, "module capability surface")
		})
	}
}
