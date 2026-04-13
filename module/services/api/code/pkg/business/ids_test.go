package business

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewID_NonZero(t *testing.T) {
	id := NewID()
	require.NotEqual(t, uuid.Nil, id)
}

func TestNewID_IsV7(t *testing.T) {
	id := NewID()
	require.Equal(t, uuid.Version(7), id.Version(), "expected UUID v7")
	require.Equal(t, uuid.RFC4122, id.Variant())
}

func TestNewID_Unique(t *testing.T) {
	seen := make(map[uuid.UUID]struct{}, 1000)
	for range 1000 {
		id := NewID()
		_, dup := seen[id]
		require.False(t, dup, "duplicate id generated: %s", id)
		seen[id] = struct{}{}
	}
}

func TestNewID_TimeOrdered(t *testing.T) {
	// v7 ids generated later should sort strictly after earlier ones at
	// millisecond granularity. Within a single millisecond the monotonic
	// counter in google/uuid still guarantees strict ordering.
	prev := NewID()
	for range 1000 {
		curr := NewID()
		require.Greater(t, curr.String(), prev.String(),
			"v7 ids must be monotonically increasing")
		prev = curr
	}
}

func TestParseID_Valid(t *testing.T) {
	original := NewID()
	parsed, err := ParseID(original.String())
	require.NoError(t, err)
	require.Equal(t, original, parsed)
}

func TestParseID_Empty(t *testing.T) {
	_, err := ParseID("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty id")
}

func TestParseID_Malformed(t *testing.T) {
	_, err := ParseID("not-a-uuid")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid id")
}
