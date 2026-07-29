package testdb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunWithPackageLockCoversTheCompleteCallback(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "integration.lock")
	contender, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contender.Close()) })

	exitCode, err := runWithPackageLock(lockPath, func() int {
		lockErr := unix.Flock(int(contender.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		require.True(t, errors.Is(lockErr, unix.EWOULDBLOCK))
		return 17
	})
	require.NoError(t, err)
	require.Equal(t, 17, exitCode)

	require.NoError(t, unix.Flock(int(contender.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	require.NoError(t, unix.Flock(int(contender.Fd()), unix.LOCK_UN))
}
