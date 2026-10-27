package infra

import (
	"context"
	"errors"
	"strconv"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/core/wool"
)

// The cross-tenant journal read (business.ModuleReadJournal). It runs on the
// control plane, where domain_events' tenant policy does not apply, so it serves
// every tenant's rows and the rows with no tenant; the caller's gate is the only
// one, and each method refuses to run outside a transaction rather than fall back
// to the request pool, where the tenant policy would make an empty answer look
// like a quiet journal.
//
// A row's key is (COALESCE(xact_id, '0'), seq): the transaction that wrote it
// (migration 42; NULL on a row from before it, read as 0, before every real id),
// then seq. A row is served only while its transaction id is below
// pg_snapshot_xmin(pg_current_snapshot()), the oldest transaction id still
// running. Any transaction that can still commit has an id at or after that
// bound, so a row below it belongs to a finished transaction, and nothing that has
// not appeared yet can sort before it: a cursor that stops at the last row served
// skips nothing. The key is the one idx_domain_events_xact_seq is built on.
//
// The same shape as the audit queue's drain (xact_id < pg_snapshot_xmin), applied
// to the journal. It holds behind a write transaction left open, and behind rows
// whose ids are ahead of the cluster after a logical restore into a new one; both
// stall the reader and lose nothing.

// journalWhere is the predicate every read of the cross-tenant journal shares:
// after $1/$2, up to $3/$4 when $3 is set, of the types $5, below the bound.
const journalWhere = `
	e.type = ANY($5::text[])
	AND (COALESCE(e.xact_id, '0'::xid8), e.seq) > ($1::text::xid8, $2::bigint)
	AND ($3::text IS NULL OR (COALESCE(e.xact_id, '0'::xid8), e.seq) <= ($3::text::xid8, $4::bigint))
	AND COALESCE(e.xact_id, '0'::xid8) < pg_snapshot_xmin(pg_current_snapshot())`

// readJournalPageSQL reads the first $6 rows in key order, then keeps the rows
// whose cumulative data fits $7 bytes, so one page is bounded in bytes as
// well as in rows; the first row is always kept even when it exceeds the bound.
const readJournalPageSQL = `
	SELECT xact::text, seq, id::text, type, tenant_id::text, occurred_at, datacontenttype, data
	FROM (
		SELECT *, sum(octet_length(data)) OVER (ORDER BY xact, seq) AS running,
               row_number() OVER (ORDER BY xact, seq) AS row_number
		FROM (
			SELECT COALESCE(e.xact_id, '0'::xid8) AS xact, e.seq, e.id, e.type, e.tenant_id,
			       COALESCE(e.event_time, e.created_at) AS occurred_at, e.datacontenttype, e.data
			FROM public.domain_events e
			WHERE` + journalWhere + `
			ORDER BY xact, e.seq
			LIMIT $6
		) page
	) sized
	WHERE running <= $7 OR row_number = 1
	ORDER BY xact, seq`

const countJournalRangeSQL = `
	SELECT count(*) FROM (
		SELECT 1 FROM public.domain_events e
		WHERE` + journalWhere + `
		LIMIT $6
	) counted`

const journalPositionVisibleSQL = `
	SELECT $1::text::xid8 < pg_snapshot_xmin(pg_current_snapshot())`

var errJournalNeedsControlPlane = errors.New("journal read: the cross-tenant journal is read inside a control-plane transaction")

// journalRangeArgs are the shared predicate's parameters $1..$5.
func journalRangeArgs(q business.JournalPageQuery) []any {
	var through, throughSeq any // NULL: no upper bound
	if q.Through != nil {
		through = uitoa(q.Through.Xact)
		throughSeq = q.Through.Seq
	}
	return []any{uitoa(q.After.Xact), q.After.Seq, through, throughSeq, q.Types}
}

func uitoa(v uint64) string { return strconv.FormatUint(v, 10) }

func (s *PostgresStore) ReadJournalPage(ctx context.Context, q business.JournalPageQuery) ([]business.JournalRow, error) {
	w := wool.Get(ctx).In("ReadJournalPage")
	if storetx.Tx(ctx) == nil {
		return nil, errJournalNeedsControlPlane
	}
	if q.Limit < 1 {
		return nil, errors.New("journal read: limit must be positive")
	}
	maxBytes := q.MaxBytes
	if maxBytes < 1 {
		maxBytes = 1<<31 - 1
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, readJournalPageSQL,
		append(journalRangeArgs(q), q.Limit, maxBytes)...)
	if err != nil {
		return nil, w.Wrapf(err, "failed to read the journal")
	}
	defer rows.Close()

	var out []business.JournalRow
	for rows.Next() {
		var (
			row         business.JournalRow
			xact        string
			tenant      *string
			occurred    time.Time
			contentType string
		)
		if err := rows.Scan(&xact, &row.Position.Seq, &row.EventID, &row.Type, &tenant, &occurred, &contentType, &row.Data); err != nil {
			return nil, w.Wrapf(err, "failed to scan a journal row")
		}
		position, err := strconv.ParseUint(xact, 10, 64)
		if err != nil {
			return nil, w.Wrapf(err, "failed to read a transaction id")
		}
		row.Position.Xact = position
		if tenant != nil {
			row.TenantID = *tenant
		}
		row.OccurredAt, row.ContentType = occurred, contentType
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "iterating journal rows")
	}
	return out, nil
}

func (s *PostgresStore) CountJournalRange(ctx context.Context, q business.JournalPageQuery, limit int) (int, error) {
	w := wool.Get(ctx).In("CountJournalRange")
	if storetx.Tx(ctx) == nil {
		return 0, errJournalNeedsControlPlane
	}
	var n int
	if err := s.getQueryExecutor(ctx).QueryRow(ctx, countJournalRangeSQL, append(journalRangeArgs(q), limit)...).Scan(&n); err != nil {
		return 0, w.Wrapf(err, "failed to count the journal range")
	}
	return n, nil
}

func (s *PostgresStore) JournalPositionVisible(ctx context.Context, pos business.JournalPosition) (bool, error) {
	w := wool.Get(ctx).In("JournalPositionVisible")
	if storetx.Tx(ctx) == nil {
		return false, errJournalNeedsControlPlane
	}
	var visible bool
	if err := s.getQueryExecutor(ctx).QueryRow(ctx, journalPositionVisibleSQL, uitoa(pos.Xact)).Scan(&visible); err != nil {
		return false, w.Wrapf(err, "failed to read the journal frontier")
	}
	return visible, nil
}
