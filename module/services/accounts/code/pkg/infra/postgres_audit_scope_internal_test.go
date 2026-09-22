package infra

import (
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// A payload filter that cannot be marshalled must fail the query rather than be
// dropped. Dropping it WIDENS the result set, and this predicate is exactly what
// an authorized collection read was narrowed to — a silent drop would hand
// organization-wide audit rows to a caller cleared for a single collection.
func TestAuditWhereRefusesAnUnbindablePayloadFilter(t *testing.T) {
	where, args, err := auditWhere(business.AuditQuery{
		OrgID:           "org-a",
		PayloadContains: map[string]any{"boundary": make(chan int)},
	}, 1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "payload filter")
	require.Empty(t, where)
	require.Empty(t, args)
}

func TestAuditWhereBindsAPayloadFilterItCanMarshal(t *testing.T) {
	where, args, err := auditWhere(business.AuditQuery{
		OrgID:           "org-a",
		PayloadContains: map[string]any{"boundary": "collection-a"},
	}, 1)

	require.NoError(t, err)
	require.Contains(t, where, "payload @> $2::jsonb")
	require.Len(t, args, 2)
	require.Contains(t, args[1], "collection-a")
}
