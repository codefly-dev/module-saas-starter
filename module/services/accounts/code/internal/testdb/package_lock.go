// Package testdb contains cross-package coordination for real-Postgres tests.
//
// Go runs package test binaries concurrently. Codefly intentionally gives
// those binaries the same managed dependency stack, so integration suites that
// reset shared tables must serialize their database mutation windows.
package testdb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// packageLockID is the signed int64 representation of the ASCII bytes
// "SAASTEST". PostgreSQL advisory locks are scoped to one database and session,
// making this a process-independent mutex for the shared integration database.
const packageLockID int64 = 0x5341415354455354

// AcquirePackageLock holds a dedicated pooled connection until release. Every
// integration package must acquire this lock before m.Run; pure unit-test
// packages remain free to execute in parallel.
func AcquirePackageLock(ctx context.Context, pool *pgxpool.Pool) (release func() error, err error) {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire integration-test lock connection: %w", err)
	}
	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", packageLockID); err != nil {
		connection.Release()
		return nil, fmt.Errorf("acquire integration-test advisory lock: %w", err)
	}

	return func() error {
		defer connection.Release()
		var unlocked bool
		if err := connection.QueryRow(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", packageLockID).Scan(&unlocked); err != nil {
			return fmt.Errorf("release integration-test advisory lock: %w", err)
		}
		if !unlocked {
			return fmt.Errorf("integration-test advisory lock was not held")
		}
		return nil
	}, nil
}
