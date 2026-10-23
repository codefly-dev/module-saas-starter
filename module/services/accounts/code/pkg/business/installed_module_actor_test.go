package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceOperationActorScopeNamesItsSources(t *testing.T) {
	named := ModuleOperationScope{ResourceKind: SourceOperationResourceKind, Actions: []string{"invoke", "read"}, ResourceIDs: []string{"source-a"}}
	require.True(t, sourceOperationActorScope(named))
	for name, scope := range map[string]ModuleOperationScope{
		"every source":   {ResourceKind: SourceOperationResourceKind, Actions: []string{"invoke"}},
		"another kind":   {ResourceKind: "tickets", Actions: []string{"read"}, ResourceIDs: []string{"source-a"}},
		"another action": {ResourceKind: SourceOperationResourceKind, Actions: []string{"invoke", "delete"}, ResourceIDs: []string{"source-a"}},
		"no action":      {ResourceKind: SourceOperationResourceKind, ResourceIDs: []string{"source-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, sourceOperationActorScope(scope))
		})
	}
}
