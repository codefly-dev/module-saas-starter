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

// The set form of the event-type filter: one array parameter, ANDed with every
// other predicate, and no predicate at all when the caller named no set.
//
// It exists because a summary over a FAMILY of event types had nothing to open.
// "New users" counts every type the registry marks as recording a person
// joining; a scalar event_type cannot name that set, so the figure was a number
// with no way to check it.
func TestAuditWhereBindsTheEventTypeSet(t *testing.T) {
	where, args, err := auditWhere(business.AuditQuery{
		OrgID:      "org-a",
		EventTypes: []string{"saas.auth.user_registered", "saas.org.member_added"},
	}, 1)

	require.NoError(t, err)
	require.Contains(t, where, "event_type = ANY($2)")
	// One parameter whatever the set's size, so the argument count never depends
	// on caller input.
	require.Len(t, args, 2)
	require.Equal(t, []string{"saas.auth.user_registered", "saas.org.member_added"}, args[1])
}

func TestAuditWhereIgnoresAnEmptyEventTypeSet(t *testing.T) {
	where, args, err := auditWhere(business.AuditQuery{OrgID: "org-a", EventTypes: []string{}}, 1)

	require.NoError(t, err)
	require.NotContains(t, where, "ANY(")
	require.Len(t, args, 1, "an empty set means the caller named none, not match nothing")
}

// The scalar and the set are both predicates, ANDed like the rest: sending both
// narrows to their intersection rather than one silently replacing the other.
func TestAuditWhereAppliesTheScalarAndTheSetTogether(t *testing.T) {
	where, args, err := auditWhere(business.AuditQuery{
		OrgID:      "org-a",
		EventType:  "saas.auth.user_registered",
		EventTypes: []string{"saas.auth.user_registered", "saas.org.member_added"},
	}, 1)

	require.NoError(t, err)
	require.Contains(t, where, "event_type = $2")
	require.Contains(t, where, "event_type = ANY($3)")
	require.Len(t, args, 3)
}
