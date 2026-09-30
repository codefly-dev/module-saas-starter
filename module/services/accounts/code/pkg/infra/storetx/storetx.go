// Package storetx reads back the store transaction a context carries. It is
// read-only: binding a transaction — and saying what authority it runs with —
// belongs to pkg/infra/internal/txbind, which the Go toolchain lets only
// pkg/infra import. pkg/business, pkg/auth/pg and the events transport read the
// transaction through Tx; none of them can relabel it, so none of them can make
// a control-plane transaction pass for a request one, or a request transaction
// bound for one tenant pass for another's.
package storetx

import (
	"context"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/infra/internal/txbind"
)

// Tx returns the store transaction ctx carries, whatever authority it runs
// with, or nil when ctx carries none.
func Tx(ctx context.Context) pgx.Tx {
	b, ok := txbind.Lookup(ctx)
	if !ok {
		return nil
	}
	return b.Tx
}
