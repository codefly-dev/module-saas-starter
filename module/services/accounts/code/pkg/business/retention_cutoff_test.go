package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A day of a retention window is 24 hours in whatever zone the process runs.
// Counted by the calendar in a zone that keeps daylight saving time, a window
// crossing the spring change would end an hour later, removing records an hour
// short of the window, and one crossing the autumn change an hour earlier.
func TestRetentionCutoffCountsDaysOf24HoursInAnyZone(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	for _, now := range []time.Time{
		time.Date(2027, time.March, 15, 12, 0, 0, 0, newYork),    // 30 days back crosses 14 March
		time.Date(2027, time.November, 15, 12, 0, 0, 0, newYork), // 30 days back crosses 7 November
	} {
		before, anyOlder, err := retentionCutoff(now, 30)
		require.NoError(t, err)
		require.True(t, anyOlder)
		want := now.Add(-30 * 24 * time.Hour)
		require.True(t, before.Equal(want), "30 days before %s is %s, not %s", now, want, before)
	}
}
