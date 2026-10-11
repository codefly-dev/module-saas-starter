package infra

import (
	"context"
	"database/sql"
	"errors"

	"accounts/pkg/infra/internal/txbind"

	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/protobuf/proto"
)

// NewEffectReceipts binds every SDK read to the tenant policy on the request
// pool. It owns no migration and cannot reach the control-plane pool.
func (s *PostgresStore) NewEffectReceipts() (*receipts.PostgresStore, func(), error) {
	// SDK Serialize holds a connection until the handler finishes. Reserve
	// capacity for the handler's own tenant transaction, even under saturation.
	if s.pool.Config().MaxConns < 2 {
		return nil, nil, errors.New("effect receipts require request pool capacity of at least two")
	}
	db := stdlib.OpenDBFromPool(s.pool)
	db.SetMaxOpenConns(int(s.pool.Config().MaxConns / 2))
	store, err := receipts.NewPostgresStore(db, receipts.WithTenantScope(func(ctx context.Context, tx *sql.Tx, tenant string) error {
		_, err := tx.ExecContext(ctx, `SELECT set_config('app.current_org_id', $1, true), set_config('app.current_user_id', '', true)`, tenant)
		return err
	}))
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return store, func() { _ = db.Close() }, nil
}

// RecordSourceOperationReceipt writes on the exact transaction committing the
// host's response. It refuses an unbound or differently bound transaction.
func (s *PostgresStore) RecordSourceOperationReceipt(ctx context.Context, store receipts.Store, response proto.Message) error {
	binding, ok := txbind.Lookup(ctx)
	effect, admitted := receipts.EffectFromContext(ctx)
	if !ok || !admitted || binding.Pool != txbind.Request || binding.OrgID != effect.Tenant {
		return errors.New("effect receipt requires its admitted tenant transaction")
	}
	return receipts.Record(ctx, store, receiptTx{binding.Tx}, response)
}

type receiptTx struct{ tx pgx.Tx }

func (t receiptTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tag, err := t.tx.Exec(ctx, query, args...)
	return receiptResult{tag}, err
}

type receiptResult struct{ tag pgconn.CommandTag }

func (r receiptResult) LastInsertId() (int64, error) {
	return 0, errors.New("Postgres does not support LastInsertId")
}
func (r receiptResult) RowsAffected() (int64, error) { return r.tag.RowsAffected(), nil }
