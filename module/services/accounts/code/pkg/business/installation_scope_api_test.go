package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseInstallationScopePreservesConsentAndRejectsMalformed(t *testing.T) {
	scope, err := ParseInstallationScope(`{"capabilities":true,"queues":["example.ingest"],"operation_audiences":{"ingest":{"audience":"example","invoke_scopes":[{"resource_kind":"records","actions":["read"]}]}}}`)
	require.NoError(t, err)
	require.True(t, scope.Capabilities)
	require.Equal(t, []string{"example.ingest"}, scope.Queues)
	require.Equal(t, []string{"read"}, scope.OperationAudiences["ingest"].InvokeScopes[0].Actions)
	empty, err := ParseInstallationScope(`{}`)
	require.NoError(t, err)
	require.False(t, empty.Capabilities)
	for _, input := range []string{"", "null", "[]", `{"tenant":"foreign"}`, `{"cross_tenant":true}`, `{"capabilites":true}`, `{"capabilities":"true"}`, `{} {}`, `{"operation_audiences":{"a":{"audience":"example","extra":true}}}`} {
		_, err := ParseInstallationScope(input)
		require.Error(t, err, input)
	}
}
