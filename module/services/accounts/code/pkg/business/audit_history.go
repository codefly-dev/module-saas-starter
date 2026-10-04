package business

import (
	"context"
	"errors"
	"fmt"
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
// monthly partitions, the way retention drops them. No row is deleted, so the
// append-only triggers stay as they are.

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
	// DropAuditPartitionsBefore drops every monthly partition whose whole range
	// is before the cutoff — the mechanism RunRetention uses.
	DropAuditPartitionsBefore(ctx context.Context, before time.Time) (int64, error)
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
	// ContentRetention is the details window: a content-class event older than
	// it has no details left to verify in the store.
	ContentRetention time.Duration
	// BatchSize is the most events one copied batch carries (default
	// DefaultAuditRelayBatchSize).
	BatchSize int
	// Through, when set, limits the copy to the partitions that end at or
	// before it, and is the drop's cutoff. Unset is every partition.
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
	// Copied is how many events this run wrote; the rest were already there.
	Copied int
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
	Partitions []AuditHistoryPartitionReport
	// Verified is true when every partition's events are in the store, each
	// copy identical to its row.
	Verified bool
	// Cutoff is the drop's cutoff: every partition ending at or before it was
	// copied and verified.
	Cutoff time.Time
	// Dropped is how many partitions were dropped; DropRefused says why none
	// were when a drop was confirmed and did not happen.
	Dropped     int64
	DropRefused string
}

// ErrAuditHistoryUnverified is returned when a run's verification failed; the
// report says where.
var ErrAuditHistoryUnverified = errors.New("audit history copy: verification failed; nothing was dropped")

const maxAuditHistoryProblems = 20

// auditHistoryPageSize is the rows one read of audit_events returns.
const auditHistoryPageSize = 1000

// auditHistoryWindow is the span the copy reads, writes and verifies at a
// time: a day keeps the read-back of a busy deployment in memory.
const auditHistoryWindow = 24 * time.Hour

// auditHistoryDetailsMargin is how far inside the content window a
// content-class event's details must still be in the store: the store expires
// details by whole day partitions, so the last day or two of the window may
// already be gone.
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
// resumable: a run reads back what the store already holds before it writes,
// copies only the events missing from it, and verifies every event, so a run
// after an interrupted one finishes what that one started. A verification
// failure blocks the drop and returns ErrAuditHistoryUnverified.
func (c *AuditHistoryCopy) Run(ctx context.Context, opts AuditHistoryCopyOptions) (AuditHistoryReport, error) {
	var report AuditHistoryReport
	partitions, err := c.partitions(ctx)
	if err != nil {
		return report, err
	}
	if len(partitions) == 0 {
		report.Verified = true
		c.cfg.Progress("no audit_events partitions to copy")
		return report, nil
	}
	if c.cfg.Through != nil {
		report.Cutoff = *c.cfg.Through
	} else {
		report.Cutoff = partitions[len(partitions)-1].To
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
	if !report.Verified {
		return report, ErrAuditHistoryUnverified
	}
	if !opts.ConfirmDrop {
		c.cfg.Progress("verified; nothing dropped without the drop confirmation")
		return report, nil
	}
	if refused, err := c.drop(ctx, &report, partitions); err != nil || refused != "" {
		report.DropRefused = refused
		if err == nil {
			err = fmt.Errorf("audit history copy: drop refused: %s", refused)
		}
		return report, err
	}
	return report, nil
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
	for from := partition.From; from.Before(partition.To); from = from.Add(auditHistoryWindow) {
		to := from.Add(auditHistoryWindow)
		if to.After(partition.To) {
			to = partition.To
		}
		if err := c.copyWindow(ctx, resolver, from, to, opts, &report); err != nil {
			return report, fmt.Errorf("audit history copy: %s [%s, %s): %w", partition.Name,
				from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), err)
		}
	}
	var rows, verified int64
	for _, count := range report.Orgs {
		rows += count.Postgres
		verified += count.Verified
	}
	c.cfg.Progress(fmt.Sprintf("%s: %d events, %d copied by this run, %d verified, %d problems",
		partition.Name, rows, report.Copied, verified, report.Failures))
	return report, nil
}

// copyWindow reads a window's rows, reads back what the store holds of it,
// writes what is missing, and verifies every row.
func (c *AuditHistoryCopy) copyWindow(ctx context.Context, resolver *AuditEventResolver, from, to time.Time, opts AuditHistoryCopyOptions, report *AuditHistoryPartitionReport) error {
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
	if !opts.VerifyOnly {
		var missing []AuditRecord
		for _, record := range records {
			if len(stored[record.Entry.ID]) == 0 {
				missing = append(missing, record)
			}
		}
		if len(missing) > 0 {
			for start := 0; start < len(missing); start += c.cfg.BatchSize {
				end := min(start+c.cfg.BatchSize, len(missing))
				if err := c.write(ctx, missing[start:end]); err != nil {
					return err
				}
				report.Copied += end - start
			}
			if stored, err = c.readBack(ctx, from, to); err != nil {
				return err
			}
		}
	}
	for _, record := range records {
		org := record.Entry.OrgID
		count := report.Orgs[org]
		count.Postgres++
		if problem := c.verify(record, stored[record.Entry.ID]); problem != "" {
			report.Failures++
			if len(report.Problems) < maxAuditHistoryProblems {
				report.Problems = append(report.Problems, fmt.Sprintf("event %s: %s", record.Entry.ID, problem))
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

// write delivers one batch as the relay does: the archive first, then the
// store. A failure leaves the rest of the window for the next run.
func (c *AuditHistoryCopy) write(ctx context.Context, records []AuditRecord) error {
	batch := AuditBatch{
		ID:           c.cfg.NewBatchID(),
		DeploymentID: c.cfg.DeploymentID,
		ComposedAt:   c.cfg.Now().UTC(),
		Records:      records,
	}
	if err := c.cfg.Archive.WriteAuditBatch(ctx, batch); err != nil {
		return fmt.Errorf("archive batch %s: %w", batch.ID, err)
	}
	if err := c.cfg.Store.AppendAuditBatch(ctx, batch); err != nil {
		return fmt.Errorf("append batch %s: %w", batch.ID, err)
	}
	return nil
}

// verify reports what is wrong with the store's copies of record, or "".
// Every copy must carry the record's envelope, retention class and details
// hash, and whatever details it carries must hash to it; a security-class
// event's details must be there, and a content-class event's while it is
// inside the content window.
func (c *AuditHistoryCopy) verify(record AuditRecord, copies []StoredAuditEvent) string {
	if len(copies) == 0 {
		return "missing from the store"
	}
	want := record.Entry
	for _, stored := range copies {
		got := stored.Entry
		switch {
		case stored.DeploymentID != c.cfg.DeploymentID:
			return fmt.Sprintf("stored under deployment %q", stored.DeploymentID)
		case got.OrgID != want.OrgID || got.ActorID != want.ActorID || got.ActorType != want.ActorType ||
			got.EventType != want.EventType || got.SchemaVersion != want.SchemaVersion ||
			got.Resource != want.Resource || got.ResourceID != want.ResourceID || got.IPAddress != want.IPAddress ||
			got.ImpersonatedBy != want.ImpersonatedBy || got.IsImpersonated != want.IsImpersonated ||
			got.ClientID != want.ClientID || !got.CreatedAt.Equal(want.CreatedAt):
			return "stored envelope differs from the row"
		case stored.Retention != record.Retention:
			return fmt.Sprintf("stored as %s, classified %s", stored.Retention, record.Retention)
		case stored.DetailsSHA256 != record.DetailsSHA256:
			return "stored details hash differs from the row's"
		case stored.HasDetails && AuditDetailsSHA256(stored.Details) != record.DetailsSHA256:
			return "stored details do not hash to the row's hash"
		case !stored.HasDetails && record.Retention == RetentionSecurity:
			return "security-class details are missing"
		case !stored.HasDetails && want.CreatedAt.After(c.cfg.Now().Add(-c.cfg.ContentRetention+auditHistoryDetailsMargin)):
			return "content details are missing inside the content window"
		}
	}
	return ""
}

// drop checks that nothing changed since verification and drops the copied
// partitions. It refuses — returning why — when a partition's rows changed, or
// when the cutoff would drop a partition this run did not verify.
func (c *AuditHistoryCopy) drop(ctx context.Context, report *AuditHistoryReport, verified []AuditHistoryPartition) (string, error) {
	covered := map[string]bool{}
	for _, partition := range verified {
		covered[partition.Name] = true
	}
	var refused string
	err := c.cfg.Source.WithControlPlane(ctx, func(ctx context.Context) error {
		current, err := c.cfg.Source.ListAuditPartitions(ctx)
		if err != nil {
			return err
		}
		for _, partition := range current {
			if !partition.To.After(report.Cutoff) && !covered[partition.Name] {
				refused = fmt.Sprintf("partition %s ends before the cutoff and was not verified by this run", partition.Name)
				return nil
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
					return nil
				}
			}
			for org, n := range counts {
				if _, known := partitionReport.Orgs[org]; !known && n > 0 {
					refused = fmt.Sprintf("partition %s changed since it was verified", partitionReport.Partition.Name)
					return nil
				}
			}
		}
		report.Dropped, err = c.cfg.Source.DropAuditPartitionsBefore(ctx, report.Cutoff)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("audit history copy: drop: %w", err)
	}
	if refused == "" {
		c.cfg.Progress(fmt.Sprintf("dropped %d partitions ending at or before %s", report.Dropped, report.Cutoff.UTC().Format(time.RFC3339)))
	}
	return refused, nil
}
