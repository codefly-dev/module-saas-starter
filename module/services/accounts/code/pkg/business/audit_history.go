package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StoredAuditEvent is one copy of an event as a store of record holds it: the
// envelope, the retention class and details hash it was written with, and the
// full details the store keeps for it — in the events row for a security-class
// event, in the details table for a content-class one, and none for a
// content-class event past the content window.
type StoredAuditEvent struct {
	DeploymentID  string
	Entry         AuditEntry // the envelope; Payload is never set
	Retention     AuditRetentionClass
	DetailsSHA256 string
	Details       string
	HasDetails    bool
}

// AuditHistoryReader reads back what a store of record holds of this
// deployment's events that occurred in [from, to), across every organization
// and the platform, every copy of a duplicated event included — the read-back
// the history copy verifies against (ADR 0009, item 7). Like every read of a
// store of record it runs no DML; on BigQuery it is a Storage Read API session.
type AuditHistoryReader interface {
	ReadStoredAuditEvents(ctx context.Context, from, to time.Time, visit func(StoredAuditEvent) error) error
}

// The one-time history copy of ADR 0009, item 7: switching a deployment from
// postgres to a swap value copies the audit_events rows it already holds into
// the store of record and the archive, verifies the copy by reading it back,
// and only then — and only when the operator confirms — drops the copied
// monthly partitions. The partitions it drops are the ones it verified, named
// one by one: the database re-checks each against what was verified inside the
// drop's own transaction, and the copy builds its receipt from the names the
// database says it dropped. No row is deleted, so the append-only triggers stay
// as they are.

// AuditHistoryPartition is one monthly partition of audit_events: the events
// that occurred in [From, To).
type AuditHistoryPartition struct {
	Name     string
	From, To time.Time
}

// AuditHistoryCursor is a position in a partition's (created_at, id) order.
type AuditHistoryCursor struct {
	CreatedAt time.Time
	ID        string
}

// AuditHistoryDropTarget is one partition the copy verified and asks to have
// dropped, with the number of rows it held when verified.
type AuditHistoryDropTarget struct {
	Name string
	Rows int64
}

// AuditHistoryTableRows is how many events one table of audit_events holds.
type AuditHistoryTableRows struct {
	Table string
	Rows  int64
}

// AuditHistoryDropRefusal is what a source returns when the database itself
// refused the drop because what the copy verified no longer holds. Nothing was
// dropped.
type AuditHistoryDropRefusal struct{ Reason string }

func (e *AuditHistoryDropRefusal) Error() string { return "drop refused: " + e.Reason }

// AuditHistorySource is audit_events as the history copy reads and retires it.
// Every method runs under WithControlPlane: the copy spans every organization.
type AuditHistorySource interface {
	WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error
	// ListAuditPartitions lists the monthly partitions, oldest first, with the
	// bounds Postgres holds for them.
	ListAuditPartitions(ctx context.Context) ([]AuditHistoryPartition, error)
	// ReadAuditHistory returns at most limit events that occurred in
	// [from, to), in (created_at, id) order and after the cursor when one is
	// given — each exactly as the queue would carry it, its payload decoded
	// with json.Number so its canonical details are the ones a live event of
	// the same row would have.
	ReadAuditHistory(ctx context.Context, from, to time.Time, after *AuditHistoryCursor, limit int) ([]AuditEntry, error)
	// CountAuditHistory counts the events that occurred in [from, to) per
	// organization ("" for the platform's).
	CountAuditHistory(ctx context.Context, from, to time.Time) (map[string]int64, error)
	// CountAuditHistoryByTable counts the events that occurred in [from, to)
	// per table that holds them: a monthly partition, the default partition, a
	// partition under another name, or audit_events itself when it is not
	// partitioned. A zero from is no lower bound.
	CountAuditHistoryByTable(ctx context.Context, from, to time.Time) ([]AuditHistoryTableRows, error)
	// DropVerifiedAuditPartitions drops exactly the named partitions, in one
	// transaction with a last lock-and-recount of each against the number of
	// rows it was verified with, and returns the names it dropped. The database
	// refuses — with an *AuditHistoryDropRefusal, dropping nothing — when a
	// partition is missing, ends after the cutoff, or holds a different number
	// of rows. It never chooses a partition itself.
	DropVerifiedAuditPartitions(ctx context.Context, cutoff time.Time, partitions []AuditHistoryDropTarget) ([]string, error)
}

// AuditHistoryCopyConfig wires a history copy.
type AuditHistoryCopyConfig struct {
	Source   AuditHistorySource
	Store    AuditStoreWriter
	Archive  AuditArchive
	ReadBack AuditHistoryReader
	// Types serves the declared half of the registry, so each event is
	// classified exactly as the relay classifies a live one.
	Types DeclaredAuditEventTypeReader
	// DeploymentID stamps every copied record, as the relay's.
	DeploymentID string
	// ContentRetention is the details window: the store keeps a content-class
	// event's details for this long (a whole number of days, at least one). The
	// copy requires the details of an event the store is certain to still hold
	// when it is run (detailsHeldUntil) and tries to supply those of one it may.
	ContentRetention time.Duration
	// BatchSize is the most events one copied batch carries (default
	// DefaultAuditRelayBatchSize).
	BatchSize int
	// Through, when set, limits the copy to the partitions whose bounds end at
	// or before it, and is the drop's cutoff; a confirmed drop needs it, and
	// refuses one that has not yet passed. Unset is every partition.
	Through *time.Time
	// Progress, when set, is told what the copy is doing.
	Progress func(message string)

	// Now and NewBatchID are seams for tests; nil uses the clock and a UUID.
	Now        func() time.Time
	NewBatchID func() string
}

// AuditHistoryCopyOptions chooses what a run may do beyond verifying.
type AuditHistoryCopyOptions struct {
	// VerifyOnly copies nothing: it verifies what the store already holds.
	VerifyOnly bool
	// ConfirmDrop drops the copied partitions once this run has verified every
	// one of them. Without it nothing is ever dropped.
	ConfirmDrop bool
	// ExpectedPartitionsSHA256 is the digest of a previous verification receipt.
	// A confirmed drop re-verifies and refuses a changed plan before any DDL.
	ExpectedPartitionsSHA256 string
}

// AuditHistoryOrgCount is one organization's events in a partition: how many
// Postgres holds and how many of those the store holds, verified.
type AuditHistoryOrgCount struct {
	Postgres int64
	Verified int64
}

// AuditHistoryPartitionReport is what a run found in one partition.
type AuditHistoryPartitionReport struct {
	Partition AuditHistoryPartition
	// EventsSHA256 binds the source event envelopes, classes and details hashes.
	EventsSHA256 string
	// Copied is how many events this run wrote because the store held none of
	// them.
	Copied int
	// Rewritten is how many events this run wrote again because the store held
	// them without details that a write supplies (a content-class event whose
	// details write was lost); the rest were already complete. An event is
	// written again only while the store holds one copy of it, so a rewrite that
	// does not supply the details is made once, not on every run. Events the
	// store refused for good are in neither Copied nor Rewritten: they are
	// problems.
	Rewritten int
	// Orgs counts the partition's events per organization ("" is the
	// platform's).
	Orgs map[string]AuditHistoryOrgCount
	// Problems are the verification failures found, the first
	// maxAuditHistoryProblems of them.
	Problems []string
	// Failures is how many there were in all.
	Failures int
}

// AuditHistoryReport is what a run did.
type AuditHistoryReport struct {
	Partitions       []AuditHistoryPartitionReport
	PartitionsSHA256 string
	// DroppedNames are the partitions the database reports it dropped; the
	// copy refuses a drop whose names are not exactly the verified ones.
	DroppedNames []string
	// Verified is true when every partition's events are in the store, each
	// copy identical to its row, and nothing older than the cutoff is held
	// outside the verified partitions. A run with no partition to verify is
	// not verified.
	Verified bool
	// Cutoff is the drop's cutoff: every partition ending at or before it was
	// copied and verified.
	Cutoff time.Time
	// Uncovered lists the tables that hold events older than the cutoff which
	// no verified partition covers — the default partition, a partition under
	// another name, one whose bounds straddle the cutoff, or audit_events
	// itself when it is not partitioned. Any makes the run unverified.
	Uncovered []AuditHistoryTableRows
	// Dropped is how many partitions were dropped; DropRefused says why none
	// were when a drop was confirmed and did not happen.
	Dropped     int64
	DropRefused string
}

// ErrAuditHistoryUnverified is returned when a run's verification failed; the
// report says where.
var ErrAuditHistoryUnverified = errors.New("audit history copy: verification failed; nothing was dropped")

// ErrAuditHistoryNothingToVerify is returned when no monthly partition ends at
// or before the cutoff and nothing older than it is held elsewhere: there is
// nothing for the run to have verified, so it claims nothing.
var ErrAuditHistoryNothingToVerify = errors.New("audit history copy: nothing to verify; no audit_events partition ends at or before the cutoff")

// ErrAuditHistoryDropRefused is returned, wrapped with the reason, when a
// confirmed drop did not happen because what was verified no longer holds or
// the approval does not match; nothing was dropped.
var ErrAuditHistoryDropRefused = errors.New("audit history copy: drop refused")

const maxAuditHistoryProblems = 20

// auditHistoryPageSize is the rows one read of audit_events returns.
const auditHistoryPageSize = 1000

// auditHistoryWindow is the span the copy reads, writes and verifies at a
// time: a day keeps the read-back of a busy deployment in memory.
const auditHistoryWindow = 24 * time.Hour

// auditHistoryDetailsMargin is how far past the instant the store is certain to
// hold an event's details the copy still tries to supply them. The store expires
// details by the day of the event (BigQuery by day partition, ClickHouse by a TTL
// merge that runs after the TTL), so for a day or two after that instant it may
// still hold them. The copy writes in that span — a missing event is written with
// its details, and so is one held without — but never fails verification there:
// the details may be legitimately gone.
const auditHistoryDetailsMargin = 2 * 24 * time.Hour

// AuditHistoryCopy is the one-time history copy.
type AuditHistoryCopy struct {
	cfg AuditHistoryCopyConfig
}

// NewAuditHistoryCopy validates cfg.
func NewAuditHistoryCopy(cfg AuditHistoryCopyConfig) (*AuditHistoryCopy, error) {
	if cfg.Source == nil || cfg.Store == nil || cfg.Archive == nil || cfg.ReadBack == nil {
		return nil, errors.New("audit history copy: source, store, archive and read-back are required")
	}
	if strings.TrimSpace(cfg.DeploymentID) == "" {
		return nil, errors.New("audit history copy: deployment id is required")
	}
	if cfg.ContentRetention < 24*time.Hour {
		return nil, errors.New("audit history copy: content retention must be at least one day")
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultAuditRelayBatchSize
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > MaxAuditRelayBatchSize {
		return nil, fmt.Errorf("audit history copy: batch size must be between 1 and %d", MaxAuditRelayBatchSize)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewBatchID == nil {
		cfg.NewBatchID = func() string { return uuid.NewString() }
	}
	if cfg.Progress == nil {
		cfg.Progress = func(string) {}
	}
	return &AuditHistoryCopy{cfg: cfg}, nil
}

// Run copies, verifies and — when confirmed and verified — drops. It is
// resumable: a run reads each window's rows, reads back what the store already
// holds, appends only the events whose stored copies are not complete — the
// events the store lacks, and those it holds without the details a write
// supplies — reads the window back again and verifies every event, so a run
// after an interrupted one finishes what that one started, a failed details
// write included. An event the store refuses for good (a PermanentRowRejection)
// is a verification failure of that event alone: the rest of its batch and of the
// run carry on. A verification failure blocks the drop and returns
// ErrAuditHistoryUnverified; a run with nothing to verify returns
// ErrAuditHistoryNothingToVerify; a drop that does not happen returns
// ErrAuditHistoryDropRefused with the reason.
func (c *AuditHistoryCopy) Run(ctx context.Context, opts AuditHistoryCopyOptions) (AuditHistoryReport, error) {
	var report AuditHistoryReport
	partitions, err := c.partitions(ctx)
	if err != nil {
		return report, err
	}
	switch {
	case c.cfg.Through != nil:
		report.Cutoff = *c.cfg.Through
	case len(partitions) > 0:
		report.Cutoff = partitions[len(partitions)-1].To
	default:
		report.Cutoff = c.cfg.Now()
	}
	resolver := NewAuditEventResolver(c.cfg.Types)
	report.Verified = true
	for _, partition := range partitions {
		partitionReport, err := c.copyPartition(ctx, resolver, partition, opts)
		report.Partitions = append(report.Partitions, partitionReport)
		if err != nil {
			report.Verified = false
			return report, err
		}
		if partitionReport.Failures > 0 {
			report.Verified = false
		}
	}
	// What the verified partitions do not cover is not verified, whatever else
	// holds: the partitions were chosen by their bounds, and an event older
	// than the cutoff may sit in a table those bounds do not name.
	if report.Uncovered, err = c.uncovered(ctx, report.Cutoff, partitions); err != nil {
		report.Verified = false
		return report, err
	}
	if len(report.Uncovered) > 0 {
		report.Verified = false
	}
	report.PartitionsSHA256 = c.planDigest(report)
	if len(partitions) == 0 && len(report.Uncovered) == 0 {
		report.Verified = false
		c.cfg.Progress("no audit_events partition ends at or before the cutoff; nothing to verify")
		return report, ErrAuditHistoryNothingToVerify
	}
	if !report.Verified {
		return report, ErrAuditHistoryUnverified
	}
	if !opts.ConfirmDrop {
		c.cfg.Progress("verified; nothing dropped without the drop confirmation")
		return report, nil
	}
	switch {
	case c.cfg.Through == nil || opts.ExpectedPartitionsSHA256 == "" || opts.ExpectedPartitionsSHA256 != report.PartitionsSHA256:
		return report, c.refuse(&report, "explicit cutoff and matching verification digest required")
	case report.Cutoff.After(c.cfg.Now()):
		return report, c.refuse(&report, "the cutoff has not passed: -through must be a completed month")
	}
	refused, err := c.drop(ctx, &report, partitions)
	if err != nil {
		return report, err
	}
	if refused != "" {
		return report, c.refuse(&report, refused)
	}
	return report, nil
}

// refuse records why a confirmed drop did not happen and returns the error that
// says so.
func (c *AuditHistoryCopy) refuse(report *AuditHistoryReport, reason string) error {
	report.DropRefused = reason
	return fmt.Errorf("%w: %s", ErrAuditHistoryDropRefused, reason)
}

// uncovered lists the tables that hold events older than the cutoff which none
// of the verified partitions covers: it counts, by table, the events in every
// stretch of time before the cutoff that the verified partitions' bounds leave
// out. The default partition and a non-partitioned audit_events are in every
// such stretch.
func (c *AuditHistoryCopy) uncovered(ctx context.Context, cutoff time.Time, verified []AuditHistoryPartition) ([]AuditHistoryTableRows, error) {
	type gap struct{ from, to time.Time } // a zero from is unbounded
	var gaps []gap
	var covered time.Time // everything before this was covered or is a gap already
	for i, partition := range verified {
		if i == 0 || partition.From.After(covered) {
			if i == 0 {
				gaps = append(gaps, gap{to: partition.From})
			} else {
				gaps = append(gaps, gap{covered, partition.From})
			}
		}
		if partition.To.After(covered) {
			covered = partition.To
		}
	}
	if len(verified) == 0 {
		gaps = append(gaps, gap{to: cutoff})
	} else {
		gaps = append(gaps, gap{covered, cutoff})
	}
	totals := map[string]int64{}
	for _, g := range gaps {
		if g.to.After(cutoff) {
			g.to = cutoff
		}
		if !g.from.IsZero() && !g.to.After(g.from) {
			continue
		}
		var found []AuditHistoryTableRows
		err := c.cfg.Source.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			found, err = c.cfg.Source.CountAuditHistoryByTable(ctx, g.from, g.to)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("audit history copy: count events outside the verified partitions: %w", err)
		}
		for _, table := range found {
			totals[table.Table] += table.Rows
		}
	}
	var out []AuditHistoryTableRows
	for table, rows := range totals {
		if rows > 0 {
			out = append(out, AuditHistoryTableRows{Table: table, Rows: rows})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out, nil
}

// partitions lists the partitions the run covers: every one, or those ending
// at or before Through.
func (c *AuditHistoryCopy) partitions(ctx context.Context) ([]AuditHistoryPartition, error) {
	var all []AuditHistoryPartition
	err := c.cfg.Source.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		all, err = c.cfg.Source.ListAuditPartitions(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("audit history copy: list partitions: %w", err)
	}
	var out []AuditHistoryPartition
	for _, partition := range all {
		if c.cfg.Through == nil || !partition.To.After(*c.cfg.Through) {
			out = append(out, partition)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.Before(out[j].From) })
	return out, nil
}

// copyPartition copies and verifies one partition, a window at a time.
func (c *AuditHistoryCopy) copyPartition(ctx context.Context, resolver *AuditEventResolver, partition AuditHistoryPartition, opts AuditHistoryCopyOptions) (AuditHistoryPartitionReport, error) {
	report := AuditHistoryPartitionReport{Partition: partition, Orgs: map[string]AuditHistoryOrgCount{}}
	digest := sha256.New()
	for from := partition.From; from.Before(partition.To); from = from.Add(auditHistoryWindow) {
		to := from.Add(auditHistoryWindow)
		if to.After(partition.To) {
			to = partition.To
		}
		if err := c.copyWindow(ctx, resolver, from, to, opts, &report, digest); err != nil {
			return report, fmt.Errorf("audit history copy: %s [%s, %s): %w", partition.Name,
				from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), err)
		}
	}
	report.EventsSHA256 = hex.EncodeToString(digest.Sum(nil))
	var rows, verified int64
	for _, count := range report.Orgs {
		rows += count.Postgres
		verified += count.Verified
	}
	c.cfg.Progress(fmt.Sprintf("%s: %d events, %d copied and %d rewritten by this run, %d verified, %d problems",
		partition.Name, rows, report.Copied, report.Rewritten, verified, report.Failures))
	return report, nil
}

// copyWindow reads a window's rows, reads back what the store holds of it,
// writes what is missing or incomplete, and verifies every row.
//
// Each event is written at most once by this call, whatever the read-back shows
// afterwards: there is one write pass, then one read-back, then verification. An
// event the store refuses for good is set aside for the rest of the call — the
// relay's rule — and reported as that event's problem; the other events of its
// batch are written without it.
func (c *AuditHistoryCopy) copyWindow(ctx context.Context, resolver *AuditEventResolver, from, to time.Time, opts AuditHistoryCopyOptions, report *AuditHistoryPartitionReport, digest hash.Hash) error {
	records, err := c.readWindow(ctx, resolver, from, to)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	stored, err := c.readBack(ctx, from, to)
	if err != nil {
		return err
	}
	written := map[string]bool{} // event id: written by this call
	refused := map[string]bool{} // event id: the store refused it for good
	if !opts.VerifyOnly {
		// An event is written when a write adds what verification would fail
		// for, or what the store may still hold: the store holds none of it, or
		// holds it without details a write supplies. The id being in the store
		// says nothing: both adapters write an event's row before its details,
		// so a failed details write leaves the row behind, and skipping on its
		// id would block verification for good.
		var pending []AuditRecord
		var rewrite []bool // parallel to pending: the store already held the event
		for _, record := range records {
			copies := stored[record.Entry.ID]
			if _, writable := c.inspect(record, copies); writable {
				pending, rewrite = append(pending, record), append(rewrite, len(copies) > 0)
			}
		}
		if len(pending) > 0 {
			for start := 0; start < len(pending); start += c.cfg.BatchSize {
				end := min(start+c.cfg.BatchSize, len(pending))
				rejected, err := c.write(ctx, pending[start:end])
				if err != nil {
					return err
				}
				for i, record := range pending[start:end] {
					id := record.Entry.ID
					written[id] = true
					switch {
					case rejected[id]:
						refused[id] = true
					case rewrite[start+i]:
						report.Rewritten++
					default:
						report.Copied++
					}
				}
			}
			if stored, err = c.readBack(ctx, from, to); err != nil {
				return err
			}
		}
	}
	for _, record := range records {
		envelope := record.Entry
		envelope.Payload, envelope.IdempotencyKey = nil, ""
		envelope.CreatedAt = envelope.CreatedAt.UTC()
		// Encoding an explicit envelope plus the content hash binds source bytes
		// without retaining payloads or a deployment-sized stream in memory.
		_ = json.NewEncoder(digest).Encode(struct {
			Entry         AuditEntry
			Retention     AuditRetentionClass
			DetailsSHA256 string
		}{envelope, record.Retention, record.DetailsSHA256})
		org := record.Entry.OrgID
		count := report.Orgs[org]
		count.Postgres++
		id := record.Entry.ID
		if problem := c.verify(record, stored[id]); problem != "" {
			switch {
			case refused[id]:
				problem = auditHistoryRefusal + "; " + problem
			case written[id]:
				problem = "written by this run, but the read-back finds it incomplete: " + problem
			}
			report.Failures++
			if len(report.Problems) < maxAuditHistoryProblems {
				report.Problems = append(report.Problems, fmt.Sprintf("event %s: %s", id, problem))
			}
		} else {
			count.Verified++
		}
		report.Orgs[org] = count
	}
	return nil
}

// readWindow reads a window's rows from audit_events, a page at a time, and
// classifies and hashes each exactly as the relay does a live event.
func (c *AuditHistoryCopy) readWindow(ctx context.Context, resolver *AuditEventResolver, from, to time.Time) ([]AuditRecord, error) {
	var records []AuditRecord
	var after *AuditHistoryCursor
	for {
		var page []AuditEntry
		err := c.cfg.Source.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			page, err = c.cfg.Source.ReadAuditHistory(ctx, from, to, after, auditHistoryPageSize)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("read audit_events: %w", err)
		}
		for _, entry := range page {
			resolved, err := resolver.Resolve(ctx, entry.EventType)
			if err != nil {
				return nil, err
			}
			record, err := NewAuditRecord(entry, resolved.RetentionClass())
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		if len(page) < auditHistoryPageSize {
			return records, nil
		}
		last := page[len(page)-1]
		after = &AuditHistoryCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
}

// readBack reads every copy the store holds of the window's events.
func (c *AuditHistoryCopy) readBack(ctx context.Context, from, to time.Time) (map[string][]StoredAuditEvent, error) {
	stored := map[string][]StoredAuditEvent{}
	err := c.cfg.ReadBack.ReadStoredAuditEvents(ctx, from, to, func(event StoredAuditEvent) error {
		stored[event.Entry.ID] = append(stored[event.Entry.ID], event)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read back the store: %w", err)
	}
	return stored, nil
}

// auditHistoryRefusal is what a problem says of an event the store refused for
// good. The store's own words are left out of it: the operator's output carries no
// provider errors.
const auditHistoryRefusal = "the store refused the event's row for good"

// write delivers one batch as the relay does: the archive first, whole and once,
// then the store. A store that reports rows it refused for good (a
// PermanentRowRejection naming an event of the batch) is sent the rest again
// without them, as many times as it names another, so one event never holds the
// others back, and returns those events by id. The batch is archived once: the
// archive holds the refused events whole, and a store write is repeated without
// them as the relay's is. A refusal that names no event of the batch, or any
// other failure, is returned as an error and leaves the rest of the window for
// the next run.
func (c *AuditHistoryCopy) write(ctx context.Context, records []AuditRecord) (map[string]bool, error) {
	batch := AuditBatch{
		ID:           c.cfg.NewBatchID(),
		DeploymentID: c.cfg.DeploymentID,
		ComposedAt:   c.cfg.Now().UTC(),
		Records:      records,
	}
	if err := c.cfg.Archive.WriteAuditBatch(ctx, batch); err != nil {
		return nil, fmt.Errorf("archive batch %s: %w", batch.ID, err)
	}
	refused := map[string]bool{}
	for todo := records; len(todo) > 0; {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("append batch %s: %w", batch.ID, err)
		}
		attempt := batch
		attempt.Records = todo
		err := c.cfg.Store.AppendAuditBatch(ctx, attempt)
		if err == nil {
			return refused, nil
		}
		rejections := map[string]bool{}
		for _, rejection := range PermanentRowRejections(err) {
			rejections[rejection.EventID] = true
		}
		var kept []AuditRecord
		for _, record := range todo {
			if id := record.Entry.ID; rejections[id] {
				refused[id] = true
			} else {
				kept = append(kept, record)
			}
		}
		if len(kept) == len(todo) {
			return nil, fmt.Errorf("append batch %s: %w", batch.ID, err)
		}
		todo = kept
	}
	return refused, nil
}

// verify reports what is wrong with the store's copies of record, or "".
// Every copy must carry the record's envelope, retention class and details
// hash, and whatever details it carries must hash to it; a security-class
// event's details must be there, and a content-class event's while the store
// is certain to hold them (detailsHeldUntil).
func (c *AuditHistoryCopy) verify(record AuditRecord, copies []StoredAuditEvent) string {
	problem, _ := c.inspect(record, copies)
	return problem
}

// inspect is the one rule for the store's copies of record, shared by what
// writes and what verifies: what verification finds wrong with them, and
// whether writing the event again would help.
//
// A write helps when the store holds none of the event, or holds it without the
// details of a content-class event: both adapters keep those in the details
// table and attach them to every copy of the event by its id, so one more
// details row completes the copies already there, and the events row the write
// appends beside them carries the same envelope. The details are a verification
// failure while the store is certain to hold them (the event is before
// detailsHeldUntil); for auditHistoryDetailsMargin after that they may still be
// held, so the write is made without a failure when they are not readable.
//
// A write does not help when a copy is wrong in itself — stored under another
// deployment, with an envelope or a retention class the row does not have, with a
// details hash that is not the row's, or with details that do not hash to it —
// because the store is append-only and every copy is verified, so the wrong copy
// would still be there, and a read of the event may return it. Nor when a
// security-class copy lacks its details: they live in that copy's own row, which
// a new row does not complete. Such a copy is a verification failure for an
// operator to look at. When copies differ in what is wrong, the reason that no
// write removes is the one returned.
//
// A write for missing details is made once: when the store holds more than one
// copy of the event, a write has already been made and did not supply them
// (refused for good, or written and not readable), and another would add a copy
// and an archive object and conclude the same. The failure stays, with the count
// of copies as its reason.
func (c *AuditHistoryCopy) inspect(record AuditRecord, copies []StoredAuditEvent) (problem string, writable bool) {
	if len(copies) == 0 {
		return "missing from the store", true
	}
	for _, stored := range copies {
		reason, fixable := c.inspectCopy(record, stored)
		switch {
		case reason != "" && !fixable:
			return reason, false
		case fixable:
			writable = true
			if problem == "" {
				problem = reason
			}
		}
	}
	if writable && len(copies) > 1 {
		if problem != "" {
			problem = fmt.Sprintf("%s; the store holds %d copies, so an earlier write did not supply them, and the event is not written again", problem, len(copies))
		}
		return problem, false
	}
	return problem, writable
}

// inspectCopy is inspect for one stored copy. It returns writable without a
// problem for details the store may still hold: written for, never failed.
func (c *AuditHistoryCopy) inspectCopy(record AuditRecord, stored StoredAuditEvent) (problem string, writable bool) {
	want, got := record.Entry, stored.Entry
	switch {
	case stored.DeploymentID != c.cfg.DeploymentID:
		return fmt.Sprintf("stored under deployment %q", stored.DeploymentID), false
	case got.OrgID != want.OrgID || got.ActorID != want.ActorID || got.ActorType != want.ActorType ||
		got.EventType != want.EventType || got.SchemaVersion != want.SchemaVersion ||
		got.Resource != want.Resource || got.ResourceID != want.ResourceID || got.IPAddress != want.IPAddress ||
		got.ImpersonatedBy != want.ImpersonatedBy || got.IsImpersonated != want.IsImpersonated ||
		got.ClientID != want.ClientID || !got.CreatedAt.Equal(want.CreatedAt):
		return "stored envelope differs from the row", false
	case stored.Retention != record.Retention:
		return fmt.Sprintf("stored as %s, classified %s", stored.Retention, record.Retention), false
	case stored.DetailsSHA256 != record.DetailsSHA256:
		return "stored details hash differs from the row's", false
	case stored.HasDetails && AuditDetailsSHA256(stored.Details) != record.DetailsSHA256:
		return "stored details do not hash to the row's hash", false
	case !stored.HasDetails && record.Retention == RetentionSecurity:
		return "security-class details are missing", false
	case stored.HasDetails:
		return "", false
	}
	// A content-class copy without details: its row says when the store may have
	// expired them.
	now, heldUntil := c.cfg.Now(), c.detailsHeldUntil(want.CreatedAt)
	switch {
	case now.Before(heldUntil):
		return "content details are missing inside the content window", true
	case now.Before(heldUntil.Add(auditHistoryDetailsMargin)):
		return "", true
	}
	return "", false
}

// detailsHeldUntil is the first instant at which the store may have expired the
// details of an event that occurred at t: before it the store holds them. The
// store expires details by the day of the event, so the count starts at the
// start of its UTC day, which no adapter beats: BigQuery drops a day partition
// once its day's start plus the retention has passed, and a ClickHouse TTL does
// not fire before the event plus the retention. It is a function of the
// retention itself, not of a fixed span cut from the window, so a short
// retention keeps its requirement: with one day, every event of today is held.
func (c *AuditHistoryCopy) detailsHeldUntil(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Add(c.cfg.ContentRetention)
}

// drop checks that nothing changed since verification and drops the verified
// partitions — those, named one by one, and no others. It refuses, returning
// why, when a verified partition is gone or has other bounds, when the cutoff
// would reach a partition this run did not verify, when a partition's rows
// changed, or when the database's own recheck, made under lock in the drop's
// transaction, disagrees. The database returns the names it dropped; a set that
// is not exactly the verified one rolls the transaction back before it commits.
func (c *AuditHistoryCopy) drop(ctx context.Context, report *AuditHistoryReport, verified []AuditHistoryPartition) (string, error) {
	byName := map[string]AuditHistoryPartition{}
	for _, partition := range verified {
		byName[partition.Name] = partition
	}
	targets := make([]AuditHistoryDropTarget, 0, len(report.Partitions))
	for _, partitionReport := range report.Partitions {
		var rows int64
		for _, count := range partitionReport.Orgs {
			rows += count.Postgres
		}
		targets = append(targets, AuditHistoryDropTarget{Name: partitionReport.Partition.Name, Rows: rows})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })

	var refused string
	var dropped []string
	err := c.cfg.Source.WithControlPlane(ctx, func(ctx context.Context) error {
		refused, dropped = "", nil
		current, err := c.cfg.Source.ListAuditPartitions(ctx)
		if err != nil {
			return err
		}
		now := map[string]AuditHistoryPartition{}
		for _, partition := range current {
			now[partition.Name] = partition
			if !partition.To.After(report.Cutoff) {
				if _, ok := byName[partition.Name]; !ok {
					refused = fmt.Sprintf("partition %s ends before the cutoff and was not verified by this run", partition.Name)
					return errAuditHistoryRollBack
				}
			}
		}
		for _, partition := range verified {
			if got, ok := now[partition.Name]; !ok || !got.From.Equal(partition.From) || !got.To.Equal(partition.To) {
				refused = fmt.Sprintf("partition %s changed since it was verified", partition.Name)
				return errAuditHistoryRollBack
			}
		}
		for _, partitionReport := range report.Partitions {
			counts, err := c.cfg.Source.CountAuditHistory(ctx, partitionReport.Partition.From, partitionReport.Partition.To)
			if err != nil {
				return err
			}
			for org, count := range partitionReport.Orgs {
				if counts[org] != count.Postgres {
					refused = fmt.Sprintf("partition %s changed since it was verified", partitionReport.Partition.Name)
					return errAuditHistoryRollBack
				}
			}
			for org, n := range counts {
				if _, known := partitionReport.Orgs[org]; !known && n > 0 {
					refused = fmt.Sprintf("partition %s changed since it was verified", partitionReport.Partition.Name)
					return errAuditHistoryRollBack
				}
			}
		}
		dropped, err = c.cfg.Source.DropVerifiedAuditPartitions(ctx, report.Cutoff, targets)
		var refusal *AuditHistoryDropRefusal
		if errors.As(err, &refusal) {
			refused = refusal.Reason
			return errAuditHistoryRollBack
		}
		if err != nil {
			return err
		}
		sort.Strings(dropped)
		if !sameNames(dropped, targets) {
			refused = fmt.Sprintf("the database dropped %v, not the verified %v; rolled back", dropped, targetNames(targets))
			return errAuditHistoryRollBack
		}
		return nil
	})
	if refused != "" {
		return refused, nil
	}
	if err != nil {
		return "", fmt.Errorf("audit history copy: drop: %w", err)
	}
	report.DroppedNames = dropped
	report.Dropped = int64(len(dropped))
	c.cfg.Progress(fmt.Sprintf("dropped %d partitions ending at or before %s", report.Dropped, report.Cutoff.UTC().Format(time.RFC3339)))
	return "", nil
}

// errAuditHistoryRollBack ends the drop's transaction without committing it; the
// reason it was refused is held beside it.
var errAuditHistoryRollBack = errors.New("audit history copy: drop transaction rolled back")

func targetNames(targets []AuditHistoryDropTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Name)
	}
	return names
}

// sameNames reports whether got, sorted, is exactly the targets' names.
func sameNames(got []string, targets []AuditHistoryDropTarget) bool {
	want := targetNames(targets) // sorted: targets are
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// planDigest excludes run timing and copied counts: copy, verify and drop runs
// of unchanged source events produce the same authorization digest.
func (c *AuditHistoryCopy) planDigest(report AuditHistoryReport) string {
	type partition struct {
		Name         string
		From, To     time.Time
		Orgs         map[string]AuditHistoryOrgCount
		EventsSHA256 string
	}
	parts := make([]partition, 0, len(report.Partitions))
	for _, p := range report.Partitions {
		parts = append(parts, partition{p.Partition.Name, p.Partition.From.UTC(), p.Partition.To.UTC(), p.Orgs, p.EventsSHA256})
	}
	body, _ := json.Marshal(struct {
		Deployment string
		Cutoff     time.Time
		Partitions []partition
	}{c.cfg.DeploymentID, report.Cutoff.UTC(), parts})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
