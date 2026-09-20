package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The tenant-facing read side of domain_events. The relay and replay paths read
// the same rows on the control plane because they fan out across tenants; these
// two run under WithOrgTx as app_tenant, so the domain_events_tenant policy is
// the floor beneath every predicate here.

const listTenantJournalSQL = `
	SELECT seq, id, type, subject, event_time, datacontenttype, data
	FROM public.domain_events
	WHERE seq > $1
	ORDER BY seq
	LIMIT $2`

// resolveTenantJournalCursorSQL answers with the seq of the named event, or the
// tenant's highest seq when it names none. The COALESCE over an empty journal
// yields 0, which is below the first identity value, so a tenant that has never
// published starts at the beginning of its own (empty) history rather than
// skipping its first event.
const resolveTenantJournalCursorSQL = `
	SELECT COALESCE(
		(SELECT seq FROM public.domain_events WHERE id = $1::uuid),
		(SELECT COALESCE(MAX(seq), 0) FROM public.domain_events))`

func (s *PostgresStore) ListTenantJournal(ctx context.Context, afterSeq int64, limit int) ([]business.JournalEntry, error) {
	w := wool.Get(ctx).In("ListTenantJournal")
	rows, err := s.getQueryExecutor(ctx).Query(ctx, listTenantJournalSQL, afterSeq, limit)
	if err != nil {
		return nil, w.Wrapf(err, "failed to read the tenant journal")
	}
	defer rows.Close()

	var out []business.JournalEntry
	for rows.Next() {
		var entry business.JournalEntry
		var eventTime *time.Time
		if err := rows.Scan(&entry.Seq, &entry.EventID, &entry.Type, &entry.Subject,
			&eventTime, &entry.DataContentType, &entry.Data); err != nil {
			return nil, w.Wrapf(err, "failed to scan a journal entry")
		}
		if eventTime != nil {
			entry.EventTime = *eventTime
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "iterating journal entries")
	}
	return out, nil
}

func (s *PostgresStore) ResolveTenantJournalCursor(ctx context.Context, eventID string) (int64, error) {
	w := wool.Get(ctx).In("ResolveTenantJournalCursor")
	// The column is UUID: binding an unparseable string aborts the transaction
	// with a type error instead of answering. A cursor is a recovery hint, never a
	// credential, so an id that is not a UUID resolves the same way an id from
	// another tenant does — to the tenant's own head.
	var named any
	if parsed, err := uuid.Parse(eventID); err == nil {
		named = parsed.String()
	}
	var cursor int64
	if err := s.getQueryExecutor(ctx).QueryRow(ctx, resolveTenantJournalCursorSQL, named).Scan(&cursor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, w.Wrapf(err, "failed to resolve the journal cursor")
	}
	return cursor, nil
}
