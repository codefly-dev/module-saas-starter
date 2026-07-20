package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// WithAuthBootstrapTx is the sole write capability exposed to the
// pre-authentication identity resolver. No tenant/user scope exists yet, so
// the normal service-postgres boundary must reject the call. The store owns
// the named, audited app_control_plane role transition and exposes only one
// serializable transaction callback, never its raw pool.
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
	return s.withControlPlaneTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context) error {
		tx, ok := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared store transaction key
		if !ok {
			return errors.New("auth bootstrap transaction is unavailable")
		}
		return fn(ctx, tx)
	})
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
