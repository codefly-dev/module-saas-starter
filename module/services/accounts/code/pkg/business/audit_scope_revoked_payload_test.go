package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The payload RevokeScope records must satisfy its own registered schema. The
// event used to declare no subject_id, so every revocation logged "audit
// payload failed registry validation": the record was kept (validation is
// advisory) but the registry no longer described it.
func TestScopeRevokedPayloadMatchesItsRegisteredSchema(t *testing.T) {
	require.NoError(t, ValidatePayload(EventScopeRevoked, map[string]any{
		"subject_id": "00000000-0000-7000-8000-0000000000a1",
		"scope_path": "root.collection",
	}))
}
