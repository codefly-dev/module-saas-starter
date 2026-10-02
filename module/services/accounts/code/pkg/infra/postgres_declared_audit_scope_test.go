package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/internal/txbind"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type declaredAuditTx struct {
	pgx.Tx
	reads int
}

func (tx *declaredAuditTx) QueryRow(context.Context, string, ...any) pgx.Row {
	tx.reads++
	return absentDeclaredAuditRow{}
}

type absentDeclaredAuditRow struct{}

func (absentDeclaredAuditRow) Scan(...any) error { return pgx.ErrNoRows }

// Main introduced this lookup after the login split. A string-key transaction
// check missed txbind's private key, opening another control-plane transaction
// on each recursive call. Even a tenant lookup must stay on its existing tx.
func TestDeclaredAuditLookupJoinsEveryBoundTransaction(t *testing.T) {
	for name, bind := range map[string]func(context.Context, pgx.Tx) context.Context{
		"request": func(ctx context.Context, tx pgx.Tx) context.Context {
			return txbind.BindRequest(ctx, tx, "tenant-a", "user-a")
		},
		"control": txbind.BindControlPlane,
		"worker":  txbind.BindWorker,
	} {
		t.Run(name, func(t *testing.T) {
			tx := &declaredAuditTx{}
			store := &infra.PostgresStore{} // no pools: opening one instead of joining is a defect
			require.NotPanics(t, func() {
				got, err := store.GetDeclaredAuditEventType(bind(context.Background(), tx), business.EventType("example.absent"))
				require.NoError(t, err)
				require.Nil(t, got)
			})
			require.Equal(t, 1, tx.reads)
		})
	}
}
