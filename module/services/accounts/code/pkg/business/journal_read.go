package business

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The cross-tenant journal read (ReadJournal on ModuleCapabilitiesService, ADR
// 0009). It is the read the platform's own reader tails: every tenant's journal
// rows, and the rows with no tenant, in an order a cursor can follow without
// skipping a row.
//
// # Position
//
// seq is the order rows were inserted, not the order their transactions commit,
// so a cursor on seq passes a row whose transaction is still open and never comes
// back for it. A position is therefore (xact_id, seq): the id of the transaction
// that wrote the row (migration 37), then seq. Rows are served in that order and
// only while xact_id is below the oldest transaction still running,
// pg_snapshot_xmin(pg_current_snapshot()). Every transaction that can still commit
// took its id at or after that bound, so a row below it belongs to a transaction
// that has finished, and no row that has not appeared yet can ever sort before one
// that has. A cursor that stops at the last row it served therefore skips nothing.
//
// The same gate holds the whole read behind a write transaction left open, and
// behind rows whose ids are ahead of the cluster after a logical restore into a
// new one. Both stall the reader without losing anything.

// JournalPosition is where a row sits in the cross-tenant read order: the id of
// the transaction that wrote it, then its seq. A row written before migration 37
// has no transaction id and sits at transaction 0, before every other row, in seq
// order. The zero value is before the first row.
type JournalPosition struct {
	Xact uint64
	Seq  int64
}

// IsZero reports whether the position is before the first row.
func (p JournalPosition) IsZero() bool { return p == JournalPosition{} }

// Less reports whether p sorts before q.
func (p JournalPosition) Less(q JournalPosition) bool {
	if p.Xact != q.Xact {
		return p.Xact < q.Xact
	}
	return p.Seq < q.Seq
}

// JournalStart is the position that means "before the oldest retained entry": a
// caller with nothing yet read names it as `after`, and DeliverAuditEvents takes it
// as `from`. It exists so the first page of a journal has a non-empty position to
// correlate on. No entry's position is ever this string (every entry's is
// "v1.<xact_id>.<seq>"), and "" is accepted as an alias of it on a read.
const JournalStart = "start"

const (
	journalPositionPrefix = "v1."
	// journalPositionMaxLength bounds what a caller can make the host parse:
	// "v1." and two decimals of at most 20 and 19 digits and a separator.
	journalPositionMaxLength = 3 + 20 + 1 + 19
)

// String is the position's wire form: an opaque, versioned string that a caller
// stores and hands back unread. It is "v1.<xact_id>.<seq>" in decimal. The zero
// position, which no entry has, is "" here; the caller-facing spelling of it is
// JournalStart. The version leads so that a later encoding is told apart from this
// one rather than misread as it.
func (p JournalPosition) String() string {
	if p.IsZero() {
		return ""
	}
	return journalPositionPrefix + strconv.FormatUint(p.Xact, 10) + "." + strconv.FormatInt(p.Seq, 10)
}

// ParseJournalPosition reads a position back. JournalStart ("start") and the empty
// string are both the zero position. Anything else must be exactly what String writes: the "v1." prefix, a
// canonical unsigned decimal transaction id and a canonical non-negative decimal
// seq, with no sign, no leading zero, no space and nothing after. A malformed
// position is InvalidArgument, never read as "the beginning": a caller that
// mangled its cursor must be told, not silently sent back over the whole journal.
func ParseJournalPosition(raw string) (JournalPosition, error) {
	if raw == "" || raw == JournalStart {
		return JournalPosition{}, nil
	}
	invalid := func() (JournalPosition, error) {
		return JournalPosition{}, status.Error(codes.InvalidArgument, "journal position is not one this host issued")
	}
	if len(raw) > journalPositionMaxLength || !strings.HasPrefix(raw, journalPositionPrefix) {
		return invalid()
	}
	xact, seq, found := strings.Cut(strings.TrimPrefix(raw, journalPositionPrefix), ".")
	if !found {
		return invalid()
	}
	x, err := strconv.ParseUint(xact, 10, 64)
	if err != nil {
		return invalid()
	}
	s, err := strconv.ParseInt(seq, 10, 64)
	if err != nil || s < 0 {
		return invalid()
	}
	position := JournalPosition{Xact: x, Seq: s}
	// Round trip: rejects the spellings strconv is lenient about (a leading
	// "+", leading zeros) and the zero position spelled out, so one position has
	// one spelling.
	if position.String() != raw {
		return invalid()
	}
	return position, nil
}

// JournalRow is one row of the cross-tenant read: the event's identity, the
// tenant it belongs to ("" for none) and the producer's own bytes.
type JournalRow struct {
	Position    JournalPosition
	EventID     string
	Type        string
	TenantID    string
	OccurredAt  time.Time
	ContentType string
	Data        []byte
}

// JournalPageQuery selects rows of the cross-tenant read. Rows are those after
// After, up to Through when it is set, of one of Types, below the visibility
// bound, in position order.
type JournalPageQuery struct {
	After   JournalPosition
	Through *JournalPosition // inclusive; nil reads to the visibility bound
	Types   []string
	Limit   int
	// MaxBytes bounds the producers' bytes one page carries. The first row is
	// always served, so a row larger than the bound still moves the cursor.
	MaxBytes int
}

// Limits of the read.
const (
	// ModuleJournalDefaultPage is the page size when the caller names none.
	ModuleJournalDefaultPage = 100
	// ModuleJournalMaxPage is the largest page, and the widest range
	// DeliverAuditEvents takes.
	ModuleJournalMaxPage = 500
	// ModuleJournalMaxPageBytes bounds the producers' bytes in one page, below
	// the transport's message limit once the data is a JSON object.
	ModuleJournalMaxPageBytes = 2 << 20
	// ModuleJournalMaxWireBytes bounds the encoded response below 4 MiB.
	ModuleJournalMaxWireBytes = 3 << 20
	moduleJournalMaxTypes     = 16
	moduleJournalMaxBindings  = 64
	moduleJournalMaxIDLength  = 256
)

// journalTypePattern is the shape domain_events.type admits (its CHECK).
var journalTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$`)

// ModuleReadJournalInput is what a caller of ReadJournal asks for.
type ModuleReadJournalInput struct {
	// After is the position the previous page ended at. JournalStart, or empty,
	// reads from before the oldest retained entry.
	After string
	// Limit is the page size; 0 is ModuleJournalDefaultPage, and a larger value
	// than ModuleJournalMaxPage is served as that maximum.
	Limit int32
	// Types are the event types to read, exactly: there is no pattern. At least
	// one is required, so no caller reads every tenant's every event by leaving
	// the filter out.
	Types []string
	// DeliveryBindings are the binding ids the caller's own delivery runs under.
	// They decide which entries are marked DeliveryMachinery.
	DeliveryBindings []string
}

// ModuleJournalEntry is one served row.
type ModuleJournalEntry struct {
	Position       string
	EventID        string
	Type           string
	OrganizationID string // "" for a row with no tenant
	OccurredAt     time.Time
	// Data is the event's JSON object. A row whose data is not a JSON object is
	// served with an empty one: the platform's own events are JSON objects, and
	// a producer's opaque bytes are not this read's to relay.
	Data map[string]any
	// DeliveryMachinery marks an entry this host recognizes as produced by the
	// caller's own delivery of the journal, so the caller need not parse saas-
	// starter's payloads to tell its own footprint from the work it carries.
	DeliveryMachinery bool
}

// ModuleReadJournalResult is a page and where to resume.
type ModuleReadJournalResult struct {
	Entries []ModuleJournalEntry
	// NextAfter is the last served entry's position, or the request's After when
	// the page is empty.
	NextAfter string
}

// ModuleReadJournal serves the cross-tenant journal to a module principal whose
// grant is CrossTenant: the same gate as a system-scoped ModuleEmitAuditEvent. It
// reads on the control plane, so it serves every tenant's rows and the rows with
// no tenant, and it never enqueues anything: the read is not a replay, and no
// subscriber is touched. Rows are in position order and only below the visibility
// bound (see the file comment), so the cursor it returns never passes a
// transaction that can still commit.
func (s *Service) ModuleReadJournal(ctx context.Context, caller ModuleCaller, in ModuleReadJournalInput) (ModuleReadJournalResult, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModuleReadJournalResult{}, fenceErr
	}
	defer releaseAuthority()

	grant, err := s.moduleCapability(ctx, caller)
	if err != nil {
		return ModuleReadJournalResult{}, err
	}
	if !grant.CrossTenant {
		return ModuleReadJournalResult{}, status.Errorf(codes.PermissionDenied,
			"principal %s may not read the journal of every tenant", caller.PrincipalID)
	}

	after, err := ParseJournalPosition(in.After)
	if err != nil {
		return ModuleReadJournalResult{}, err
	}
	if in.Limit < 0 {
		return ModuleReadJournalResult{}, status.Error(codes.InvalidArgument, "limit must not be negative")
	}
	limit := int(in.Limit)
	if limit == 0 {
		limit = ModuleJournalDefaultPage
	}
	if limit > ModuleJournalMaxPage {
		limit = ModuleJournalMaxPage
	}
	types, err := journalReadTypes(in.Types)
	if err != nil {
		return ModuleReadJournalResult{}, err
	}
	bindings, err := journalDeliveryBindings(in.DeliveryBindings)
	if err != nil {
		return ModuleReadJournalResult{}, err
	}

	var rows []JournalRow
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		rows, err = s.store.ReadJournalPage(ctx, JournalPageQuery{
			After: after, Types: types, Limit: limit, MaxBytes: ModuleJournalMaxPageBytes,
		})
		return err
	}); err != nil {
		return ModuleReadJournalResult{}, status.Error(codes.Internal, "read the journal: "+err.Error())
	}

	out := ModuleReadJournalResult{NextAfter: in.After, Entries: make([]ModuleJournalEntry, 0, len(rows))}
	for _, row := range rows {
		out.Entries = append(out.Entries, ModuleJournalEntry{
			Position:          row.Position.String(),
			EventID:           row.EventID,
			Type:              row.Type,
			OrganizationID:    row.TenantID,
			OccurredAt:        row.OccurredAt,
			Data:              journalDataObject(row.ContentType, row.Data),
			DeliveryMachinery: JournalRowIsDeliveryMachinery(row, caller.PrincipalID, bindings),
		})
		out.NextAfter = row.Position.String()
	}
	return out, nil
}

func journalReadTypes(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one event type is required")
	}
	if len(raw) > moduleJournalMaxTypes {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d event types may be read at once", moduleJournalMaxTypes)
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if len(t) > moduleJournalMaxIDLength || !journalTypePattern.MatchString(t) {
			return nil, status.Errorf(codes.InvalidArgument, "%q is not an event type", t)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

func journalDeliveryBindings(raw []string) (map[string]struct{}, error) {
	if len(raw) > moduleJournalMaxBindings {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d delivery bindings may be named", moduleJournalMaxBindings)
	}
	out := make(map[string]struct{}, len(raw))
	for _, id := range raw {
		if id == "" || len(id) > moduleJournalMaxIDLength {
			return nil, status.Error(codes.InvalidArgument, "a delivery binding id is not empty and at most 256 bytes")
		}
		out[id] = struct{}{}
	}
	return out, nil
}

// journalDataObject is the event's data as a JSON object, or an empty one when it
// is not.
func journalDataObject(contentType string, data []byte) map[string]any {
	out := map[string]any{}
	if contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// JournalRowIsDeliveryMachinery reports whether the row carries an audit entry
// that is the footprint of the caller's own delivery of the journal: the audit the
// delivery itself generates, which, if it were delivered as ordinary work, would
// produce more of itself. It is one function so the rule is stated once.
//
// Only a row of type saas.audit.recorded.v1 carries an audit entry, and the rule
// reads that entry:
//
//   - saas.module.operation_context_minted, whose binding_id is one of
//     deliveryBindings: the headless Work Context a delivery runs under, issued
//     for one of the caller's delivery bindings.
//   - saas.module.delegated_audience_exchange, whose binding_id is one of
//     deliveryBindings: an exchange on one of the caller's delivery bindings.
//   - saas.module.work_context_minted whose actor is the caller's own module
//     principal: the caller's own refresh of its Work Context. Another module's
//     mint is that module's, not the caller's.
//
// Everything else is false, including a row of another type and a record that
// cannot be read: unmarked work is delivered, never skipped.
func JournalRowIsDeliveryMachinery(row JournalRow, callerPrincipal string, deliveryBindings map[string]struct{}) bool {
	if row.Type != AuditRecordedEventType {
		return false
	}
	entry, err := AuditEntryFromJournal(row.Data)
	if err != nil {
		return false
	}
	return AuditEntryIsDeliveryMachinery(entry, callerPrincipal, deliveryBindings)
}

// AuditEntryIsDeliveryMachinery is the rule of JournalRowIsDeliveryMachinery on
// the audit entry itself.
func AuditEntryIsDeliveryMachinery(entry AuditEntry, callerPrincipal string, deliveryBindings map[string]struct{}) bool {
	switch entry.EventType {
	case EventModuleOperationContextMint, EventDelegatedAudienceExchange:
		principal := entry.ActorID
		if entry.EventType == EventDelegatedAudienceExchange {
			principal, _ = entry.Payload["module_principal_id"].(string)
			kind, _ := entry.Payload["binding_kind"].(string)
			if kind != "operation" {
				return false
			}
		}
		if callerPrincipal == "" || principal == "" || canonicalPrincipalID(principal) != canonicalPrincipalID(callerPrincipal) {
			return false
		}
		binding, ok := entry.Payload["binding_id"].(string)
		if !ok || binding == "" {
			return false
		}
		_, ours := deliveryBindings[binding]
		return ours
	case EventModuleWorkContextMint:
		return callerPrincipal != "" && entry.ActorID != "" &&
			canonicalPrincipalID(entry.ActorID) == canonicalPrincipalID(callerPrincipal)
	default:
		return false
	}
}

// canonicalPrincipalID is a principal id in the one text form the audit trail
// records, so an id compares equal however it was spelled.
func canonicalPrincipalID(id string) string {
	if parsed, err := uuid.Parse(id); err == nil {
		return parsed.String()
	}
	return id
}
