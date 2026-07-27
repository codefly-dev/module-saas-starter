package infra

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const authBootstrapMaxAttempts = 8

var retryableAuthBootstrapConstraints = map[string]struct{}{
	"idx_users_primary_email_lower":   {},
	"user_identities_provider_unique": {},
}

// WithAuthBootstrapTx is the sole write capability exposed to the
// pre-authentication identity resolver. No tenant/user scope exists yet, so
// the normal service-postgres boundary must reject the call. The store owns
// the named, audited app_control_plane role transition and exposes only one
// serializable transaction callback, never its raw pool.
//
// The callback is deliberately database-only and may be replayed from the
// beginning when Postgres aborts a serializable transaction. This retry
// belongs here rather than in service-postgres's general Writer API: an
// arbitrary application callback may have non-database side effects and
// therefore cannot be retried safely without an explicit contract.
func (s *PostgresStore) WithAuthBootstrapTx(
	ctx context.Context,
	fn func(context.Context, pgx.Tx) error,
) error {
	if fn == nil {
		return errors.New("auth bootstrap transaction callback is required")
	}
	// Bootstrap is also a deliberate control-plane elevation. Keep it visible
	// in the same per-callsite audit counters as direct WithControlPlane use.
	recordControlPlane(ctx)
	for attempt := 1; attempt <= authBootstrapMaxAttempts; attempt++ {
		err := s.withControlPlaneTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context) error {
			tx, ok := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared store transaction key
			if !ok {
				return errors.New("auth bootstrap transaction is unavailable")
			}
			return fn(ctx, tx)
		})
		if err == nil || !isRetryableAuthBootstrapError(err) || attempt == authBootstrapMaxAttempts {
			return err
		}
		wool.Get(ctx).In("WithAuthBootstrapTx").Debug(
			"retrying serializable authentication transaction",
			wool.Field("attempt", attempt+1),
			wool.Field("max_attempts", authBootstrapMaxAttempts),
		)
		if err := waitForAuthBootstrapRetry(ctx, attempt); err != nil {
			return err
		}
	}
	panic("unreachable authentication transaction retry state")
}

func isRetryableAuthBootstrapError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code == "40001" || pgErr.Code == "40P01" {
		return true
	}
	if pgErr.Code != "23505" {
		return false
	}
	_, ok := retryableAuthBootstrapConstraints[pgErr.ConstraintName]
	return ok
}

func waitForAuthBootstrapRetry(ctx context.Context, attempt int) error {
	// Full jitter prevents a burst of authentication requests from repeatedly
	// colliding in lockstep. The maximum cumulative wait is comfortably below
	// Resolver's five-second operation deadline.
	base := 2 * time.Millisecond << min(attempt-1, 6)
	delay := base + time.Duration(rand.Int64N(int64(base)+1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WithAuthLookupTx is the read-only sibling used by pre-authentication token
// and API-key resolution. Repeatable read gives one coherent policy snapshot;
// AccessModeReadOnly is enforced by Postgres even though pgx.Tx itself has an
// Exec method.
func (s *PostgresStore) WithAuthLookupTx(
	ctx context.Context,
	fn func(context.Context, pgx.Tx) error,
) error {
	if fn == nil {
		return errors.New("auth lookup transaction callback is required")
	}
	recordControlPlane(ctx)
	return s.withControlPlaneTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	}, func(ctx context.Context) error {
		tx, ok := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared store transaction key
		if !ok {
			return errors.New("auth lookup transaction is unavailable")
		}
		return fn(ctx, tx)
	})
}
