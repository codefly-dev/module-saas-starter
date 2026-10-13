package business

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DeliverAuditEventsMethod is the host operation that appends a range of the
// journal's audit records to the warehouse (ADR 0009). It is an operation, not a
// sweep: it names one range, it carries the effect identity AuditDeliveryEffectID
// derives from it, and its receipt is read back rather than inferred.
const DeliverAuditEventsMethod = "/saas.accounts.v1.AuditService/DeliverAuditEvents"

// AuditDeliveryMaxRange is the most audit records one delivery takes: the page
// maximum of the journal read that produced the range.
const AuditDeliveryMaxRange = ModuleJournalMaxPage

// Reasons a delivery is refused, on the ErrorInfo of its refusal.
const (
	// AuditDeliveryRangeNotVisibleReason: the range's end is not yet below the
	// oldest transaction still running, so rows could still appear inside it.
	AuditDeliveryRangeNotVisibleReason = "AUDIT_RANGE_NOT_VISIBLE"
	// AuditDeliveryRangeTooWideReason: more audit records than a delivery takes.
	AuditDeliveryRangeTooWideReason = "AUDIT_RANGE_TOO_WIDE"
	// AuditDeliveryNotWarehouseReason: this host's audit storage is not the
	// warehouse, so the journal holds no audit records to deliver.
	AuditDeliveryNotWarehouseReason = "AUDIT_BACKEND_NOT_WAREHOUSE"
	// AuditDeliveryRecordRefusedReason: a journal row's record contradicts its
	// row, and is not delivered.
	AuditDeliveryRecordRefusedReason = "AUDIT_RECORD_REFUSED"
)

// AuditDeliveryEffectID is the effect identity of the delivery of the range that
// starts after from: one range, one receipt. from is the position or JournalStart,
// exactly as the request spells it.
func AuditDeliveryEffectID(from string) string {
	return AuditRecordedEventType + "/" + from
}

// AuditDeliveryResult is what a delivery committed.
type AuditDeliveryResult struct {
	// Delivered is the number of audit records of the range appended to the
	// storage.
	Delivered int
}

// DeliverAuditEvents appends every saas.audit.recorded.v1 record with a position in
// (from, to] to the audit storage, once the range is final, and commits the range's
// receipt.
//
// It is called only as an admitted operation: the receipt guard has placed the
// effect in ctx, under the tenant of the installation's operation context, and the
// effect's id must be the range's (AuditDeliveryEffectID). Replay of a committed
// range is answered by the guard from its receipt and never reaches here.
//
//   - The range is read with the visibility rule of the journal read. If to is not
//     yet below the oldest transaction still running the range could still gain a
//     row, so the delivery is refused (FailedPrecondition, AUDIT_RANGE_NOT_VISIBLE)
//     and nothing is appended; a range more than AuditDeliveryMaxRange records wide
//     is refused (InvalidArgument, AUDIT_RANGE_TOO_WIDE) before anything is.
//   - Each record is appended to the storage with its organization taken from its
//     journal row, never from the caller; a record that names another organization
//     than its row is refused. An entry with no organization has none.
//   - Delivery is at least once: a range delivered twice, or two ranges that
//     overlap, append a record twice, and every read of the storage returns each
//     entry once, by entry id. A failed append is Unavailable and no receipt is
//     committed, so the whole range is attempted again.
//   - commit runs in a transaction of the effect's tenant, after every append, and
//     records the receipt there. A range with no audit records still commits one.
//   - It emits no audit of its own: delivering the audit trail must not write to it.
func (s *Service) DeliverAuditEvents(ctx context.Context, from, to string, commit func(context.Context, AuditDeliveryResult) error) (AuditDeliveryResult, error) {
	effect, ok := receipts.EffectFromContext(ctx)
	if !ok || effect.Method != DeliverAuditEventsMethod || effect.Tenant == "" || effect.ID != AuditDeliveryEffectID(from) || commit == nil {
		return AuditDeliveryResult{}, status.Error(codes.FailedPrecondition, "audit delivery runs only as an admitted operation for its own range")
	}
	if from == "" {
		return AuditDeliveryResult{}, status.Error(codes.InvalidArgument, `from names the start of the range; the beginning is "start"`)
	}
	fromPos, err := ParseJournalPosition(from)
	if err != nil {
		return AuditDeliveryResult{}, err
	}
	toPos, err := ParseJournalPosition(to)
	if err != nil {
		return AuditDeliveryResult{}, err
	}
	if to == "" || to == JournalStart || !fromPos.Less(toPos) {
		return AuditDeliveryResult{}, status.Error(codes.InvalidArgument, "to must be a position after from")
	}
	if s.auditReads == nil || s.auditReads.Backend() != AuditBackendWarehouse {
		return AuditDeliveryResult{}, moduleRefusal(codes.FailedPrecondition, AuditDeliveryNotWarehouseReason,
			"this host keeps audit in Postgres; there is no journal record to deliver")
	}
	storage := s.auditReads.Storage()

	query := JournalPageQuery{After: fromPos, Through: &toPos, Types: []string{AuditRecordedEventType}}
	var visible bool
	var width int
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		if visible, err = s.store.JournalPositionVisible(ctx, toPos); err != nil || !visible {
			return err
		}
		width, err = s.store.CountJournalRange(ctx, query, AuditDeliveryMaxRange+1)
		return err
	}); err != nil {
		return AuditDeliveryResult{}, status.Error(codes.Unavailable, "read the journal range: "+err.Error())
	}
	if !visible {
		return AuditDeliveryResult{}, moduleRefusal(codes.FailedPrecondition, AuditDeliveryRangeNotVisibleReason,
			"the end of the range is not yet below the oldest transaction still running")
	}
	if width > AuditDeliveryMaxRange {
		return AuditDeliveryResult{}, moduleRefusal(codes.InvalidArgument, AuditDeliveryRangeTooWideReason,
			fmt.Sprintf("the range holds more than %d audit records", AuditDeliveryMaxRange))
	}

	var result AuditDeliveryResult
	query.Limit, query.MaxBytes = ModuleJournalDefaultPage, ModuleJournalMaxPageBytes
	for page := 0; page <= AuditDeliveryMaxRange; page++ {
		var rows []JournalRow
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			rows, err = s.store.ReadJournalPage(ctx, query)
			return err
		}); err != nil {
			return AuditDeliveryResult{}, status.Error(codes.Unavailable, "read the journal range: "+err.Error())
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			entry, err := journalDecodeAudit(row)
			if err != nil {
				return AuditDeliveryResult{}, moduleRefusal(codes.FailedPrecondition, AuditDeliveryRecordRefusedReason, err.Error())
			}
			if err := storage.Append(ctx, entry); err != nil {
				return AuditDeliveryResult{}, status.Error(codes.Unavailable, "append audit entry "+entry.ID+": "+err.Error())
			}
			result.Delivered++
		}
		query.After = rows[len(rows)-1].Position
	}

	// The receipt commits in a transaction of the effect's tenant, which is what
	// binds it to the installation the operation ran for. The organizations of the
	// records are the rows', and have no part in it.
	if err := s.store.WithOrgTx(ctx, effect.Tenant, func(ctx context.Context) error {
		return commit(ctx, result)
	}); err != nil {
		if status.Code(err) == codes.Unknown {
			return AuditDeliveryResult{}, status.Error(codes.Unavailable, "commit the delivery receipt: "+err.Error())
		}
		return AuditDeliveryResult{}, err
	}
	return result, nil
}

// ErrJournalRowDisagrees is the refusal of a journal row whose own record
// contradicts the row.
var ErrJournalRowDisagrees = errors.New("journal row disagrees with its audit record")

func journalRowDisagrees(row JournalRow, why string) error {
	return fmt.Errorf("%w: event %s: %s", ErrJournalRowDisagrees, row.EventID, why)
}

// journalDecodeAudit reads the audit entry out of a saas.audit.recorded.v1 row. The
// organization is the row's: the publish guard binds a row's tenant to its
// transaction, and a record that names another organization than its row is
// refused, not corrected.
func journalDecodeAudit(row JournalRow) (AuditEntry, error) {
	if row.Type != AuditRecordedEventType {
		return AuditEntry{}, journalRowDisagrees(row, "not an audit record")
	}
	entry, err := AuditEntryFromJournal(row.Data)
	if err != nil {
		return AuditEntry{}, journalRowDisagrees(row, err.Error())
	}
	if row.EventID != AuditJournalEventID(entry.ID) {
		return AuditEntry{}, journalRowDisagrees(row, "the event id is not the entry's")
	}
	if !strings.EqualFold(entry.OrgID, row.TenantID) {
		return AuditEntry{}, journalRowDisagrees(row, "the record names another organization than the row")
	}
	entry.OrgID = row.TenantID
	return entry, nil
}
