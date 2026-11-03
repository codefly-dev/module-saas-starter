package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/datasource/objectstore"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

// The daily audit archive.
//
// `ExportAuditLog` has always let an administrator download their own
// organisation's audit log on request. Nothing ran it on a schedule into a
// store outside the database, so the only copy of the audit trail was the
// Postgres table it was written to — which the same operator who can change a
// record can also change.
//
// The archive closes that. Its contract, and the parts of it that are
// deliberately not what a reader might assume:
//
//   - One object per complete UTC day, under the configured prefix, in the
//     object-storage service's own bucket. Accounts names the key; the bucket,
//     its credentials and its retention posture belong to the composition.
//   - Scheduled by the host's daily sweep, performed by a leased job. The sweep
//     only enqueues; the export itself runs off the tick, under a lease, with a
//     retry budget, so a slow store or a large day cannot wedge the sweep.
//   - Idempotent per day, twice over and independently: the job's idempotency
//     key is the day, so the sweep may enqueue the same day for ever without
//     producing a second job, and the write is create-if-absent, so even a job
//     that outlived its own record cannot replace an archived day's bytes.
//   - A receipt per archived day in the audit log itself — the day, the key, the
//     digest, the byte and row counts — so "was yesterday archived, and is the
//     object in the bucket the object we wrote" is answerable from the trail
//     rather than from the bucket alone.
//
// **Sealed means create-if-absent plus a digest receipt, not WORM.** The object
// cannot be silently replaced by this host, and a replacement by anything else
// is detectable against the receipt's digest. It is not immutable: that needs an
// object-lock/retention policy on the bucket, which belongs to the
// infrastructure that provisions it and does not exist yet. Until it does, the
// archive is tamper-*evident* and not tamper-*proof*, and module/AUDIT_METRICS.md
// says so in the same words.
const (
	AuditArchiveQueue         = "audit_archive"
	AuditArchiveTopic         = "audit.archive.export"
	AuditArchiveSource        = "saas.audit"
	AuditArchiveSchemaVersion = 1
	// Eight attempts over the generic worker's backoff outlasts an
	// object-storage outage of a few hours, which is the failure this retries
	// for; beyond that an operator has a dead letter to look at rather than a
	// job quietly retrying for a week.
	AuditArchiveMaxAttempts = 8
	AuditArchiveContentType = "application/json"
	// auditArchiveCatchUpDays is how many complete days each sweep offers. The
	// enqueue is deduplicated by day, so offering a window rather than only
	// yesterday is what makes a host that was down over a weekend archive the
	// days it missed instead of losing them silently. A day older than the
	// window is an operator's `ExportAuditLog` to run by hand.
	auditArchiveCatchUpDays = 7
)

// AuditArchiveRetryDelay spaces the attempts. The archive is not on anyone's
// request path and holds no ordering key, so unlike the privacy workflow it can
// afford to wait: the schedule spends the eight-attempt budget over roughly
// four hours, which outlasts an ordinary object-store incident, and anything
// longer than that is a dead letter an operator should be looking at rather
// than a job still hoping.
func AuditArchiveRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
		time.Hour,
		time.Hour,
		time.Hour,
	}
	index := 0
	if attempt > 0 {
		index = min(int(attempt-1), len(schedule)-1)
	}
	return schedule[index]
}

// AuditArchiveStore is the object store the archive writes to: one
// create-if-absent write, and nothing else. The host's object-storage client
// implements it (objectstore.HostStore); a test scripts it.
//
// PutIfAbsent must report objectstore.ErrObjectExists — matched here through
// ErrAuditArchiveObjectExists — for a key already present, because that is the
// signal the day is already archived rather than a failure to archive it.
type AuditArchiveStore interface {
	PutIfAbsent(ctx context.Context, key, contentType string, body []byte) (etag string, err error)
}

// ErrAuditArchiveObjectExists is the sentinel the archiver recognises as "this
// day is already in the store". It IS the object store's own error rather than
// a second one translated into it: a sentinel the implementation does not
// return is a branch that never runs, and that branch is the second of the two
// idempotency guards — the one that holds when the first has been reaped.
var ErrAuditArchiveObjectExists = objectstore.ErrObjectExists

// AuditArchive is the configured destination, resolved once at startup. An
// empty Prefix means no archive is configured and nothing is scheduled.
type AuditArchive struct {
	// Prefix is the key prefix every archived day is written under, from
	// AUDIT_ARCHIVE_URI. It names no bucket: PutIfAbsent lands in the
	// object-storage service's own bucket, so a bucket named here would be a
	// claim this host cannot honour.
	Prefix string
	// Store is the writer. Nil with a non-empty Prefix is a misconfiguration
	// the boot refuses, not a silent no-op.
	Store AuditArchiveStore
}

// ParseAuditArchiveURI reads AUDIT_ARCHIVE_URI into a key prefix.
//
// It accepts a bare prefix ("audit/archive", "audit/archive/") and refuses
// anything that names a bucket, a host or a scheme. The object-storage
// service's Put carries no bucket — the bucket is the composition's, configured
// on that service — so accepting `s3://somewhere-else/audit` would write to a
// bucket the operator did not name while reading as though it had. A boot
// refusal naming the problem is the only honest answer.
func ParseAuditArchiveURI(raw string) (string, error) {
	prefix := strings.TrimSpace(raw)
	if prefix == "" {
		return "", nil
	}
	if parsed, err := url.Parse(prefix); err == nil && (parsed.Scheme != "" || parsed.Host != "") {
		return "", fmt.Errorf(
			"AUDIT_ARCHIVE_URI must be a key prefix inside the composed object-storage service's own bucket, not a URI naming a bucket or host (%q): the bucket, its credentials and its retention policy belong to the composition, and this host cannot write to another one",
			raw)
	}
	if strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf("AUDIT_ARCHIVE_URI must not begin with %q: a store key prefix is literal, and a leading slash makes every key one too (%q)", "/", raw)
	}
	return strings.TrimSuffix(prefix, "/"), nil
}

// SetAuditArchive wires the archive destination. Called once at startup.
func (s *Service) SetAuditArchive(archive AuditArchive) { s.auditArchive = archive }

// AuditArchiveConfigured reports whether a destination is wired, which is what
// decides whether the sweep and the worker run at all.
func (s *Service) AuditArchiveConfigured() bool {
	return s.auditArchive.Prefix != "" && s.auditArchive.Store != nil
}

// auditArchivePayload is the job payload: the day, and nothing else. The day
// determines the key, the query window and the idempotency key, so a job
// carrying anything more would let those three disagree.
type auditArchivePayload struct {
	Day string `json:"day"`
}

// auditArchiveDay is the canonical spelling of a day everywhere in the
// archive: the payload, the idempotency key, the object key and the receipt.
const auditArchiveDay = "2006-01-02"

// auditArchiveActor is the actor on the receipt. No person asked for a
// scheduled archive, and attributing it to one would put a name in the trail
// that did nothing.
const auditArchiveActor = "system"

// AuditArchiveKey is the object key for one day. The date path makes a
// lifecycle rule, a listing and a human all read the same way, and the day is
// in the file name too so an object pulled out of the bucket on its own still
// says which day it is.
func AuditArchiveKey(prefix, day string) string {
	return fmt.Sprintf("%s/%s/audit-%s.json", prefix, strings.ReplaceAll(day, "-", "/"), day)
}

// newAuditArchiveJob offers one day for archiving. The idempotency key is the
// day, which is what makes the sweep safe to run on every tick for ever: the
// job store admits the first offer of a day and drops every later one.
func newAuditArchiveJob(day string) (*jobsv1.EnqueueJobRequest, error) {
	payload, err := json.Marshal(auditArchivePayload{Day: day})
	if err != nil {
		return nil, err
	}
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:          AuditArchiveQueue,
		Topic:          AuditArchiveTopic,
		Source:         AuditArchiveSource,
		IdempotencyKey: "audit-archive-" + day,
		SchemaVersion:  AuditArchiveSchemaVersion,
		Payload:        payload,
		ContentType:    AuditArchiveContentType,
		MaxAttempts:    AuditArchiveMaxAttempts,
	}}
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	return request, nil
}

func decodeAuditArchiveEnvelope(envelope *jobsv1.JobEnvelope) (string, error) {
	if err := jobs.ValidateCommand(envelope); err != nil {
		return "", err
	}
	scope, global := envelope.GetScope().GetValue().(*jobsv1.JobScope_Global)
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		!global || !scope.Global ||
		envelope.GetQueue() != AuditArchiveQueue ||
		envelope.GetTopic() != AuditArchiveTopic ||
		envelope.GetSource() != AuditArchiveSource ||
		envelope.GetSchemaVersion() != AuditArchiveSchemaVersion ||
		envelope.GetContentType() != AuditArchiveContentType {
		return "", errors.New("audit archive: unexpected job routing")
	}
	var payload auditArchivePayload
	if err := json.Unmarshal(envelope.GetPayload(), &payload); err != nil {
		return "", err
	}
	if _, err := time.Parse(auditArchiveDay, payload.Day); err != nil {
		return "", fmt.Errorf("audit archive: %q is not a day", payload.Day)
	}
	// The day decides the key and the window, so a job whose payload day and
	// whose idempotency key disagree would archive one day under another's name.
	if envelope.GetIdempotencyKey() != "audit-archive-"+payload.Day {
		return "", errors.New("audit archive: the job's day and its idempotency key disagree")
	}
	return payload.Day, nil
}

// ScheduleAuditArchive offers every complete day in the catch-up window for
// archiving and returns how many were newly accepted. It performs no export: a
// sweep that exported inline would hold the daily tick for as long as the
// slowest store takes, and an object-storage outage would stall the retention
// pass that shares the tick.
//
// now is the caller's clock, so a test need not wait for midnight.
func (s *Service) ScheduleAuditArchive(ctx context.Context, now time.Time) (int, error) {
	if !s.AuditArchiveConfigured() {
		return 0, nil
	}
	producer, ok := s.store.(jobs.Producer)
	if !ok {
		return 0, errors.New("audit archive: the store cannot enqueue jobs")
	}
	w := wool.Get(ctx).In("ScheduleAuditArchive")
	var offered int
	var failures []error
	// Yesterday first, then backwards: the day an operator is waiting for is
	// the most recent one, and a window whose older end fails should not stop
	// it being offered.
	for back := 1; back <= auditArchiveCatchUpDays; back++ {
		day := now.UTC().AddDate(0, 0, -back).Format(auditArchiveDay)
		request, err := newAuditArchiveJob(day)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", day, err))
			continue
		}
		accepted, err := producer.EnqueueJob(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", day, err))
			continue
		}
		// A day already offered is the ordinary case on every tick after the
		// first, and says nothing; a newly inserted one is worth counting.
		if accepted.GetDisposition() == jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED {
			offered++
			w.Info("audit archive day offered", wool.Field("day", day))
		}
	}
	return offered, errors.Join(failures...)
}

// NewAuditArchiveJobHandler exports one leased day.
func (s *Service) NewAuditArchiveJobHandler() jobs.Handler {
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		day, err := decodeAuditArchiveEnvelope(envelope)
		if err != nil {
			// A job that can never execute is a permanent failure: retrying a
			// malformed envelope eight times only delays the dead letter an
			// operator has to look at anyway.
			return errors.Join(err, jobs.NewProcessingError(
				"audit_archive.invalid_job", "unexpected audit archive job", false))
		}
		return s.archiveAuditDay(ctx, day)
	}
}

// archiveAuditDay exports one complete UTC day and records its receipt.
func (s *Service) archiveAuditDay(ctx context.Context, day string) error {
	w := wool.Get(ctx).In("archiveAuditDay").With(wool.Field("day", day))
	if !s.AuditArchiveConfigured() {
		// The job outlived its configuration — an operator cleared
		// AUDIT_ARCHIVE_URI while a day was still queued. There is nowhere to
		// write, and no amount of retrying creates one.
		return jobs.NewProcessingError("audit_archive.not_configured",
			"no audit archive destination is configured", false)
	}
	body, rows, err := s.auditDayExport(ctx, day)
	if err != nil {
		return err
	}
	key := AuditArchiveKey(s.auditArchive.Prefix, day)
	digest := sha256.Sum256(body)
	etag, err := s.auditArchive.Store.PutIfAbsent(ctx, key, AuditArchiveContentType, body)
	switch {
	case errors.Is(err, ErrAuditArchiveObjectExists):
		// The day is archived. This is the second idempotency guard doing its
		// job — a job that outlived its own record, or two workers racing the
		// same lease hand-off — and it is a success, not a failure. No second
		// receipt: the first archiving already wrote one, and a receipt whose
		// digest describes bytes this run did not write would be a lie.
		w.Info("audit archive day was already in the store", wool.Field("key", key))
		return nil
	case err != nil:
		// Retryable: an object store that refuses today may accept in a minute,
		// and the attempt budget is what decides when to give up.
		return fmt.Errorf("write the audit archive object %s: %w", key, err)
	}
	// The receipt goes in the audit log itself, under the control plane, after
	// the object is durable. Written before, it would claim an archive that may
	// not exist; the opposite order can only lose a receipt for an object that
	// is there, which the digest in the store's own metadata and a re-run make
	// recoverable.
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, auditArchiveActor, ActorTypeSystem, EventAuditArchiveExported, "audit_archive", key, "",
			map[string]any{
				"day":        day,
				"object_key": key,
				"digest":     "sha256:" + hex.EncodeToString(digest[:]),
				"bytes":      len(body),
				"events":     rows,
				"etag":       etag,
			})
	}); err != nil {
		// The object is written and the receipt is not. Retrying re-runs the
		// export, meets ErrObjectExists and returns without a receipt, so a
		// retry cannot repair this — say so plainly and let it dead-letter
		// where an operator sees it, rather than burn the attempt budget.
		w.Error("the audit archive object is written but its receipt is not", wool.ErrField(err), wool.Field("key", key))
		return errors.Join(err, jobs.NewProcessingError("audit_archive.receipt_failed",
			"the archive object is written but its receipt could not be recorded", false))
	}
	w.Info("audit archive day exported",
		wool.Field("key", key), wool.Field("bytes", len(body)), wool.Field("events", rows))
	return nil
}

// auditDayExport serialises one complete UTC day of the whole platform's audit
// log, and returns the bytes and how many events they carry.
//
// It is the platform-wide export — every organisation plus the control plane —
// because that is what an archive of the audit trail is. The per-organisation
// download `ExportAuditLog` serves is a different question with a different
// authorisation, and reusing it here would archive one tenant at a time under
// a tenant's own scope.
func (s *Service) auditDayExport(ctx context.Context, day string) ([]byte, int, error) {
	from, err := time.Parse(auditArchiveDay, day)
	if err != nil {
		return nil, 0, jobs.NewProcessingError("audit_archive.invalid_day", "not a day", false)
	}
	from = from.UTC()
	resolver := s.AuditEventResolver()
	entries := make([]auditExportEntry, 0, 256)
	pageToken := ""
	for {
		until := from.AddDate(0, 0, 1)
		page, next, _, err := s.QueryAuditLog(ctx, AuditQuery{
			From:      &from,
			To:        &until,
			PageSize:  500,
			PageToken: pageToken,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("query the audit log for %s: %w", day, err)
		}
		for _, entry := range page {
			resolved, err := resolver.Resolve(ctx, entry.EventType)
			if err != nil {
				return nil, 0, fmt.Errorf("resolve %q for %s: %w", entry.EventType, day, err)
			}
			entries = append(entries, auditEntryToExport(entry, resolved))
		}
		if next == "" || len(page) == 0 {
			break
		}
		pageToken = next
	}
	// A day with no events is still archived, as an empty array. The absence of
	// an object must mean "not archived" and nothing else: a day skipped
	// because it was quiet is indistinguishable, in the bucket, from a day
	// whose export never ran.
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, 0, err
	}
	return body, len(entries), nil
}
