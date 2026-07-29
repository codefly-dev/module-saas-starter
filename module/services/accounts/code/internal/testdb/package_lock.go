// Package testdb contains cross-package coordination for integration tests.
package testdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const packageLockFilename = "codefly-saas-starter-accounts-integration.lock"

// RunWithPackageLock serializes the complete setup, test, and teardown lifecycle
// of packages that own Codefly dependencies.
func RunWithPackageLock(run func() int) (int, error) {
	return runWithPackageLock(
		filepath.Join(os.TempDir(), packageLockFilename),
		run,
	)
}

func runWithPackageLock(lockPath string, run func() int) (exitCode int, err error) {
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 1, fmt.Errorf("open integration-test lifecycle lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		_ = lockFile.Close()
		return 1, fmt.Errorf("acquire integration-test lifecycle lock: %w", err)
	}

	defer func() {
		unlockErr := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		closeErr := lockFile.Close()
		if unlockErr != nil {
			unlockErr = fmt.Errorf("release integration-test lifecycle lock: %w", unlockErr)
		}
		if closeErr != nil {
			closeErr = fmt.Errorf("close integration-test lifecycle lock: %w", closeErr)
		}
		err = errors.Join(err, unlockErr, closeErr)
	}()

	return run(), nil
}
