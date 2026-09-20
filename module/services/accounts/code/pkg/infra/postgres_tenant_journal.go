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
// run under WithOrgTx as app_tenant, so the domain_events_tenant policy is the
// floor beneath every predicate here — and each statement pins tenant_id itself
// on top of that floor, so a caller that ever reached them with RLS bypassed
// still reads one tenant rather than all of them.

// listTenantJournalSQL reads entry identities only. The payload is left for
// LoadTenantJournalPayloads, after visibility has been resolved: this page is
// read before anyone knows what the caller may see, so reading `data` here would
// charge a reader with no access for every byte the tenant publishes.
const listTenantJournalSQL = `
	SELECT seq, id, type, subject, event_time
	FROM public.domain_events
	WHERE tenant_id = $1::uuid
	  AND seq > $2
	ORDER BY seq
	LIMIT $3`

// loadTenantJournalPayloadsSQL reads the payloads of entries the caller is
// already authorized for. The size predicate is what bounds one poll: data has
// no CHECK constraint on domain_events (the 1 MiB cap lives on
// job_messages.payload, which this path never touches), so without it a single
// page could materialize an unbounded number of bytes per connected client.
const loadTenantJournalPayloadsSQL = `
	SELECT id, datacontenttype, data
	FROM public.domain_events
	WHERE tenant_id = $1::uuid
	  AND id = ANY($2::uuid[])
	  AND octet_length(data) <= $3`

const resolveTenantJournalCursorSQL = `
	SELECT seq FROM public.domain_events WHERE tenant_id = $1::uuid AND id = $2::uuid`

const tenantJournalHeadSQL = `
	SELECT COALESCE(MAX(seq), 0) FROM public.domain_events WHERE tenant_id = $1::uuid`

func (s *PostgresStore) ListTenantJournal(ctx context.Context, orgID string, afterSeq int64, limit int) ([]business.JournalEntry, error) {
	w := wool.Get(ctx).In("ListTenantJournal")
	rows, err := s.getQueryExecutor(ctx).Query(ctx, listTenantJournalSQL, orgID, afterSeq, limit)
	if err != nil {
		return nil, w.Wrapf(err, "failed to read the tenant journal")
	}
	defer rows.Close()

	var out []business.JournalEntry
	for rows.Next() {
		var entry business.JournalEntry
		var eventTime *time.Time
		if err := rows.Scan(&entry.Seq, &entry.EventID, &entry.Type, &entry.Subject, &eventTime); err != nil {
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

func (s *PostgresStore) LoadTenantJournalPayloads(
	ctx context.Context, orgID string, eventIDs []string, maxBytes int,
) (map[string]business.JournalPayload, error) {
	w := wool.Get(ctx).In("LoadTenantJournalPayloads")
	if len(eventIDs) == 0 {
		return nil, nil
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, loadTenantJournalPayloadsSQL, orgID, eventIDs, maxBytes)
	if err != nil {
		return nil, w.Wrapf(err, "failed to read journal payloads")
	}
	defer rows.Close()

	out := make(map[string]business.JournalPayload, len(eventIDs))
	for rows.Next() {
		var id string
		var payload business.JournalPayload
		if err := rows.Scan(&id, &payload.ContentType, &payload.Data); err != nil {
			return nil, w.Wrapf(err, "failed to scan a journal payload")
		}
		out[id] = payload
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "iterating journal payloads")
	}
	return out, nil
}

func (s *PostgresStore) ResolveTenantJournalCursor(ctx context.Context, orgID, eventID string) (int64, bool, error) {
	w := wool.Get(ctx).In("ResolveTenantJournalCursor")
	executor := s.getQueryExecutor(ctx)

	// The column is UUID: binding an unparseable string aborts the transaction
	// with a type error instead of answering. A cursor is a recovery hint, never a
	// credential, so an id that is not a UUID is simply not resolved, exactly as
	// an id from another tenant is not.
	if parsed, err := uuid.Parse(eventID); err == nil {
		var seq int64
		err := executor.QueryRow(ctx, resolveTenantJournalCursorSQL, orgID, parsed.String()).Scan(&seq)
		if err == nil {
			return seq, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, w.Wrapf(err, "failed to resolve the journal cursor")
		}
	}

	var head int64
	if err := executor.QueryRow(ctx, tenantJournalHeadSQL, orgID).Scan(&head); err != nil {
		return 0, false, w.Wrapf(err, "failed to read the journal head")
	}
	return head, false, nil
}
