package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type workerPoolConfig struct {
	role            string
	applicationName string
	logScope        string
}

// newWorkerPoolFromURL is the single role-assumption boundary for privileged
// background workers. The supplied role is a compile-time constant at every
// call site and is still identifier-quoted before it reaches SQL.
func newWorkerPoolFromURL(
	ctx context.Context,
	connectionURL string,
	worker workerPoolConfig,
) (*pgxpool.Pool, error) {
	if worker.role == "" || worker.applicationName == "" || worker.logScope == "" {
		return nil, errors.New("worker pool role, application name, and log scope are required")
	}
	w := wool.Get(ctx).In(worker.logScope)
	config, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		return nil, w.Wrapf(err, "failed to parse connection string")
	}
	config.MaxConns = 4
	config.ConnConfig.RuntimeParams["application_name"] = worker.applicationName
	roleSQL := pgx.Identifier{worker.role}.Sanitize()
	config.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		if _, err := conn.Exec(ctx, "SET ROLE "+roleSQL); err != nil {
			wool.Get(ctx).In(worker.logScope+".BeforeAcquire").Warn(
				"SET ROLE failed",
				wool.ErrField(err),
			)
			return false
		}
		return true
	}
	config.AfterRelease = func(conn *pgx.Conn) bool {
		_, err := conn.Exec(context.Background(), "RESET ROLE")
		return err == nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, w.Wrapf(err, "failed to create worker pool")
	}
	var currentRole string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&currentRole); err != nil {
		pool.Close()
		return nil, w.Wrapf(err, "worker role check failed")
	}
	if currentRole != worker.role {
		pool.Close()
		return nil, fmt.Errorf("worker pool assumed %q, expected %q", currentRole, worker.role)
	}
	return pool, nil
}
