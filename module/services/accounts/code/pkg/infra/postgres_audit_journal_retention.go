package infra

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/internal/txbind"

	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/protobuf/proto"
)

type auditJournalRange struct{ from, to business.JournalPosition }

// auditDeliveryCoverage reads the SDK's committed protobuf response, rather
// than inferring an end from its count or effect id. A legacy or inconsistent
// receipt proves nothing and leaves the journal's only copy in place.
func auditDeliveryCoverage(effect string, digest, response []byte) (auditJournalRange, bool) {
	var out gen.DeliverAuditEventsResponse
	if proto.Unmarshal(response, &out) != nil || out.Status != "committed" || out.From == "" || out.To == "" ||
		out.EffectId != effect || effect != business.AuditDeliveryEffectID(out.From) {
		return auditJournalRange{}, false
	}
	from, err := business.ParseJournalPosition(out.From)
	if err != nil {
		return auditJournalRange{}, false
	}
	to, err := business.ParseJournalPosition(out.To)
	if err != nil || !from.Less(to) {
		return auditJournalRange{}, false
	}
	expected, err := receipts.RequestDigest(&gen.DeliverAuditEventsRequest{From: out.From, To: out.To})
	if err != nil || !bytes.Equal(expected, digest) {
		return auditJournalRange{}, false
	}
	return auditJournalRange{from, to}, true
}

// DeleteDeliveredAuditJournal runs on the existing retention transaction and
// SDK receipt table. Positions are global across tenants: the receipt's tenant
// is the delivery installation, not the organization on any particular row.
func (s *PostgresStore) DeleteDeliveredAuditJournal(ctx context.Context, before time.Time) (deleted, oldUnproven int64, err error) {
	binding, ok := txbind.Lookup(ctx)
	if !ok || binding.Pool != txbind.ControlPlane {
		return 0, 0, errors.New("audit journal retention requires a control-plane transaction")
	}
	rows, err := binding.Tx.Query(ctx, `SELECT effect_id, request_digest, response
		FROM public.codefly_effect_receipts WHERE method = $1 AND tenant <> ''`, business.DeliverAuditEventsMethod)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var ranges []auditJournalRange
	for rows.Next() {
		var effect string
		var digest, response []byte
		if err := rows.Scan(&effect, &digest, &response); err != nil {
			return 0, 0, err
		}
		if span, valid := auditDeliveryCoverage(effect, digest, response); valid {
			ranges = append(ranges, span)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	rows.Close()

	// Collapse overlapping or touching intervals so a sequential delivery does
	// not cause one DELETE per historical receipt. A gap always stays a gap.
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].from.Less(ranges[j].from) })
	merged := ranges[:0]
	for _, span := range ranges {
		if len(merged) == 0 || merged[len(merged)-1].to.Less(span.from) {
			merged = append(merged, span)
		} else if merged[len(merged)-1].to.Less(span.to) {
			merged[len(merged)-1].to = span.to
		}
	}
	for _, span := range merged {
		tag, err := binding.Tx.Exec(ctx, `DELETE FROM public.domain_events e
			WHERE e.type = $1 AND e.created_at < $2
			AND (COALESCE(e.xact_id, '0'::xid8), e.seq) > ($3::text::xid8, $4::bigint)
			AND (COALESCE(e.xact_id, '0'::xid8), e.seq) <= ($5::text::xid8, $6::bigint)`,
			business.AuditRecordedEventType, before, uitoa(span.from.Xact), span.from.Seq, uitoa(span.to.Xact), span.to.Seq)
		if err != nil {
			return 0, 0, err
		}
		deleted += tag.RowsAffected()
	}
	err = binding.Tx.QueryRow(ctx, `SELECT count(*) FROM public.domain_events
		WHERE type = $1 AND created_at < $2`, business.AuditRecordedEventType, before).Scan(&oldUnproven)
	if err != nil {
		return 0, 0, err
	}
	return deleted, oldUnproven, nil
}
