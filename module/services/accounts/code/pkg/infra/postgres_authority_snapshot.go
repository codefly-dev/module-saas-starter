package infra

// One authority question, one transaction.
//
// The core reads the issuer's live state through its sources, and each source
// is a store Read of its own: Check reads the organization's authorization
// revision, the owner's seal, the owner's epoch and every actor hop's, the
// operation binding, then the audience; ListResources adds its own decision
// read, and a child Issue verifies the parent and every hop it re-signs. Over
// this store every Read was a control-plane transaction — a pool acquire,
// BEGIN, SET LOCAL ROLE, the queries, COMMIT and the session reset — so one
// question took the shared control-plane pool a dozen times or more, and a
// burst of questions from one caller queued behind each other, and behind
// delivery, until the caller's deadline.
//
// WithAuthoritySnapshot answers every Read of one question from one REPEATABLE
// READ, READ ONLY transaction instead. It is not a cache: the snapshot is
// opened when the question starts and closed when it is answered, nothing it
// read is kept, and the next question opens its own. A revocation committed
// before a question starts is what that question reads, exactly as it was when
// each source opened its own transaction; one committed while a question is
// being answered is read by the next one, which is also what the per-source
// reads gave (each read only what had committed when it began). The one
// difference is in the snapshot's favour: every source now answers from the
// same instant, so a question can no longer read a revision from before a
// change beside a seal from after it.
//
// Inside that one transaction a query asked again with the same arguments
// answers exactly what it answered the first time: the snapshot admits no
// commit and makes no write. So it is sent once and its rows replayed
// (snapshotReads); the core asks the same few questions — an audience's
// declaration, a principal's epoch, the organization's revision — over and
// over while it verifies one capability, and every one of them was a round
// trip.
//
// The transaction is taken from a pool of its own, before anything else the
// question needs. Holding a control-plane connection while waiting for
// another would let a burst exhaust the control-plane pool against itself —
// a single-use capability is consumed on a control-plane transaction while
// the question's snapshot is still open — so the order is always snapshot
// first, control plane second, and nothing that holds a control-plane
// connection opens a snapshot.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"accounts/pkg/infra/storetx"
)

type authoritySnapshotKey struct{}

// authoritySnapshot is one question's transaction. ended is set by a write
// made inside the question: what is read after it is read fresh, never the
// state from before the question's own write.
type authoritySnapshot struct {
	store *PostgresStore
	ctx   context.Context
	reads *snapshotReads
	ended atomic.Bool
}

// WithAuthoritySnapshot runs fn, a single authority question, with every
// authority-core Read under its context answered from one read-only
// transaction (see the file comment). Nothing else under the context joins
// it: a store call outside the core's Read opens its own transaction as
// before. A question already inside a snapshot of this store keeps it. A store
// with no snapshot pool (a test's zero value) runs fn unchanged.
func (s *PostgresStore) WithAuthoritySnapshot(ctx context.Context, fn func(context.Context) error) error {
	if snap, ok := ctx.Value(authoritySnapshotKey{}).(*authoritySnapshot); ok && snap.store == s {
		return fn(ctx)
	}
	if s.authorityRead == nil {
		return fn(ctx)
	}
	recordControlPlane(ctx)
	return withControlPlaneTxOn(ctx, s.authorityRead, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(txCtx context.Context) error {
		// txCtx carries the transaction for the core's Read alone; fn runs
		// under ctx, so nothing else the question does joins a read-only
		// transaction.
		tx := storetx.Tx(txCtx)
		reads := &snapshotReads{q: tx, typeMap: tx.Conn().TypeMap(), results: map[string]*snapshotResult{}}
		return fn(context.WithValue(ctx, authoritySnapshotKey{}, &authoritySnapshot{store: s, ctx: txCtx, reads: reads}))
	})
}

// snapshotOf is the open snapshot of store s that ctx carries, if any.
func snapshotOf(ctx context.Context, s *PostgresStore) *authoritySnapshot {
	snap, ok := ctx.Value(authoritySnapshotKey{}).(*authoritySnapshot)
	if !ok || snap.store != s || snap.ended.Load() {
		return nil
	}
	return snap
}

// snapshotReads is a snapshot's query executor: each distinct query, with its
// arguments, is sent once and its rows replayed after that. Only a query whose
// every argument has a key (snapshotKey) is replayed; any other is sent every
// time. A failed query is never kept.
type snapshotReads struct {
	q       QueryExecutor
	typeMap *pgtype.Map
	mu      sync.Mutex
	results map[string]*snapshotResult
	// sent and replayed count the statements this snapshot sent to the
	// database and the ones it answered from a kept result.
	sent, replayed int
}

type snapshotResult struct {
	fields []pgconn.FieldDescription
	rows   [][][]byte
	tag    pgconn.CommandTag
}

// Exec is sent as it is: the transaction is read only, so a write fails there.
func (r *snapshotReads) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return r.q.Exec(ctx, sql, args...)
}

func (r *snapshotReads) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	key, keyed := snapshotKey(sql, args)
	if keyed {
		r.mu.Lock()
		kept := r.results[key]
		if kept != nil {
			r.replayed++
		}
		r.mu.Unlock()
		if kept != nil {
			return &replayedRows{result: kept, typeMap: r.typeMap}, nil
		}
	}
	r.mu.Lock()
	r.sent++
	r.mu.Unlock()
	rows, err := r.q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	result, err := readResult(rows)
	if err != nil {
		return nil, err
	}
	if keyed {
		r.mu.Lock()
		r.results[key] = result
		r.mu.Unlock()
	}
	return &replayedRows{result: result, typeMap: r.typeMap}, nil
}

func (r *snapshotReads) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := r.Query(ctx, sql, args...)
	return replayedRow{rows: rows, err: err}
}

// readResult reads every row, each value copied out of the connection's
// buffer, which the next read reuses.
func readResult(rows pgx.Rows) (*snapshotResult, error) {
	defer rows.Close()
	result := &snapshotResult{}
	for rows.Next() {
		raw := rows.RawValues()
		row := make([][]byte, len(raw))
		for i, value := range raw {
			row[i] = bytes.Clone(value) // nil stays nil: SQL NULL
		}
		result.rows = append(result.rows, row)
	}
	result.fields = append([]pgconn.FieldDescription(nil), rows.FieldDescriptions()...)
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result.tag = rows.CommandTag()
	return result, nil
}

// snapshotKey is the query and its arguments, each typed, so two calls share
// a key exactly when they send the same statement with the same values. An
// argument of any other type has no key, and its query is not replayed.
func snapshotKey(sql string, args []any) (string, bool) {
	var key bytes.Buffer
	key.WriteString(strconv.Quote(sql))
	for _, arg := range args {
		switch v := arg.(type) {
		case string:
			key.WriteString(" s" + strconv.Quote(v))
		case uuid.UUID:
			key.WriteString(" u" + v.String())
		case *uuid.UUID:
			if v == nil {
				key.WriteString(" u-")
			} else {
				key.WriteString(" u*" + v.String())
			}
		case nil:
			key.WriteString(" nil")
		case []uuid.UUID:
			key.WriteString(" U" + strconv.Itoa(len(v)))
			for _, id := range v {
				key.WriteString(":" + id.String())
			}
		case []string:
			key.WriteString(" S" + strconv.Itoa(len(v)))
			for _, item := range v {
				key.WriteString(":" + strconv.Quote(item))
			}
		case int, int32, int64, uint64, bool:
			fmt.Fprintf(&key, " %T:%v", v, v)
		default:
			return "", false
		}
	}
	return key.String(), true
}

// replayedRows serves a kept result as pgx.Rows. Every Scan reads a fresh copy
// of the values, so nothing a caller does to what it scanned reaches the
// result the next call replays.
type replayedRows struct {
	result  *snapshotResult
	typeMap *pgtype.Map
	next    int
	closed  bool
}

func (r *replayedRows) Close()                                       { r.closed = true }
func (r *replayedRows) Err() error                                   { return nil }
func (r *replayedRows) CommandTag() pgconn.CommandTag                { return r.result.tag }
func (r *replayedRows) FieldDescriptions() []pgconn.FieldDescription { return r.result.fields }
func (r *replayedRows) Conn() *pgx.Conn                              { return nil }
func (r *replayedRows) TypeMap() *pgtype.Map                         { return r.typeMap }

func (r *replayedRows) Next() bool {
	if r.closed || r.next >= len(r.result.rows) {
		r.closed = true
		return false
	}
	r.next++
	return true
}

func (r *replayedRows) current() [][]byte {
	row := r.result.rows[r.next-1]
	out := make([][]byte, len(row))
	for i, value := range row {
		out[i] = bytes.Clone(value)
	}
	return out
}

func (r *replayedRows) Scan(dest ...any) error {
	return pgx.ScanRow(r.typeMap, r.result.fields, r.current(), dest...)
}

func (r *replayedRows) RawValues() [][]byte { return r.current() }

// Values is not used by the authority core's store, and is refused rather
// than half-implemented.
func (r *replayedRows) Values() ([]any, error) {
	return nil, errors.New("authority snapshot: Values is not served from a replayed result")
}

// replayedRow is QueryRow over replayedRows: the first row, or pgx.ErrNoRows.
type replayedRow struct {
	rows pgx.Rows
	err  error
}

func (r replayedRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		return pgx.ErrNoRows
	}
	return r.rows.Scan(dest...)
}
