package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// expires_in is seconds from the response, not from the signature. Token
// issuance and response construction are separated by database work, so the
// two differ by however long that work took.
func TestExpiresInSecondsReportsRemainingNotGrantedLifetime(t *testing.T) {
	const granted = 3 * time.Minute

	// Responding immediately reports very nearly the whole grant, never more.
	fresh := expiresInSeconds(time.Now().Add(granted))
	require.LessOrEqual(t, fresh, int64(granted.Seconds()))
	require.Greater(t, fresh, int64(granted.Seconds())-5)

	// The regression this guards: work done between signing and responding must
	// come off the reported value rather than be handed to the client as time it
	// does not have.
	delayed := expiresInSeconds(time.Now().Add(granted - 2*time.Second))
	require.LessOrEqual(t, delayed, int64(granted.Seconds())-2)

	// A token whose life already ran out reports zero, not a negative count.
	require.Zero(t, expiresInSeconds(time.Now().Add(-5*time.Second)))

	// Truncating rather than rounding keeps the value an upper bound: a caller
	// is never told it has a second the token will not honour.
	require.Zero(t, expiresInSeconds(time.Now().Add(500*time.Millisecond)))
}
