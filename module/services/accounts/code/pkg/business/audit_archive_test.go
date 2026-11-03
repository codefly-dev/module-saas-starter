package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/datasource/objectstore"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

// AUDIT_ARCHIVE_URI names a prefix inside the composed object-storage
// service's own bucket. A value naming a bucket, a host or a scheme is refused
// at the boot that reads it: the service's write carries no bucket, so
// accepting `s3://somewhere/audit` would write to the composition's bucket
// while reading as though it had honoured the operator's.
func TestParseAuditArchiveURI(t *testing.T) {
	for _, testCase := range []struct {
		raw, want string
		refused   bool
	}{
		{raw: "", want: ""},
		{raw: "   ", want: ""},
		{raw: "audit/archive", want: "audit/archive"},
		{raw: "audit/archive/", want: "audit/archive"},
		{raw: " audit/archive ", want: "audit/archive"},
		{raw: "s3://acme-audit/archive", refused: true},
		{raw: "gs://acme-audit", refused: true},
		{raw: "https://store.example.com/audit", refused: true},
		{raw: "//acme-audit/archive", refused: true},
		{raw: "/audit/archive", refused: true},
	} {
		t.Run(fmt.Sprintf("%q", testCase.raw), func(t *testing.T) {
			prefix, err := ParseAuditArchiveURI(testCase.raw)
			if testCase.refused {
				require.Error(t, err)
				require.Contains(t, err.Error(), "AUDIT_ARCHIVE_URI")
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, prefix)
		})
	}
}

// The key puts the day in the path and in the file name: the path so a
// lifecycle rule and a listing read the same way, the file name so an object
// pulled out of the bucket on its own still says which day it is.
func TestAuditArchiveKey(t *testing.T) {
	require.Equal(t,
		"audit/archive/2026/11/02/audit-2026-11-02.json",
		AuditArchiveKey("audit/archive", "2026-11-02"))
}

// The job's idempotency key is the day, which is what makes the sweep safe to
// run on every tick for ever.
func TestAuditArchiveJobIsKeyedByItsDay(t *testing.T) {
	request, err := newAuditArchiveJob("2026-11-02")
	require.NoError(t, err)
	require.Equal(t, "audit-archive-2026-11-02", request.GetJob().GetIdempotencyKey())
	require.Equal(t, AuditArchiveQueue, request.GetJob().GetQueue())
	require.True(t, request.GetJob().GetScope().GetGlobal())
}

// An envelope whose payload day and whose idempotency key disagree would
// archive one day under another's name, so it is refused rather than run.
func TestAuditArchiveEnvelopeRefusesAMismatchedDay(t *testing.T) {
	request, err := newAuditArchiveJob("2026-11-02")
	require.NoError(t, err)
	job := request.GetJob()

	envelope := &jobsv1.JobEnvelope{
		Id: NewIDString(), Direction: job.GetDirection(), Scope: job.GetScope(),
		Queue: job.GetQueue(), Topic: job.GetTopic(), Source: job.GetSource(),
		IdempotencyKey: job.GetIdempotencyKey(), SchemaVersion: job.GetSchemaVersion(),
		Payload: job.GetPayload(), ContentType: job.GetContentType(),
		State:       jobsv1.JobState_JOB_STATE_PENDING,
		MaxAttempts: job.GetMaxAttempts(),
	}
	day, err := decodeAuditArchiveEnvelope(envelope)
	require.NoError(t, err)
	require.Equal(t, "2026-11-02", day)

	envelope.IdempotencyKey = "audit-archive-2026-11-03"
	_, err = decodeAuditArchiveEnvelope(envelope)
	require.ErrorContains(t, err, "disagree")

	envelope.IdempotencyKey = job.GetIdempotencyKey()
	envelope.Payload = []byte(`{"day":"not-a-day"}`)
	_, err = decodeAuditArchiveEnvelope(envelope)
	require.ErrorContains(t, err, "not a day")

	envelope.Payload = job.GetPayload()
	envelope.Queue = "something-else"
	_, err = decodeAuditArchiveEnvelope(envelope)
	require.ErrorContains(t, err, "unexpected job routing")
}

// The retry schedule spends the attempt budget over hours, not seconds: the
// failure it retries for is an object store being down, and the archive is on
// nobody's request path.
func TestAuditArchiveRetryDelayGrowsAndSettles(t *testing.T) {
	require.Equal(t, time.Minute, AuditArchiveRetryDelay(0))
	require.Equal(t, time.Minute, AuditArchiveRetryDelay(1))
	require.Equal(t, 5*time.Minute, AuditArchiveRetryDelay(2))
	require.Equal(t, time.Hour, AuditArchiveRetryDelay(AuditArchiveMaxAttempts))
	require.Equal(t, time.Hour, AuditArchiveRetryDelay(1000))

	var total time.Duration
	for attempt := range uint32(AuditArchiveMaxAttempts) {
		total += AuditArchiveRetryDelay(attempt)
	}
	require.Greater(t, total, 3*time.Hour, "the budget must outlast an ordinary store incident")
	require.Less(t, total, 12*time.Hour, "and must reach a dead letter the same day")
}

// Nothing is scheduled and nothing is written without a configured
// destination, and a store with no prefix is not a destination either: both
// halves have to be present or the feature is off.
func TestAuditArchiveIsOffWithoutBothHalves(t *testing.T) {
	for _, archive := range []AuditArchive{
		{},
		{Prefix: "audit/archive"},
		{Store: &scriptedArchiveStore{}},
	} {
		svc := &Service{auditArchive: archive}
		require.False(t, svc.AuditArchiveConfigured())
		offered, err := svc.ScheduleAuditArchive(context.Background(), time.Now())
		require.NoError(t, err)
		require.Zero(t, offered)
	}
}

// scriptedArchiveStore records what was written and can refuse as the store
// does for a key already present.
type scriptedArchiveStore struct {
	objects map[string][]byte
	exists  bool
	err     error
}

func (s *scriptedArchiveStore) PutIfAbsent(_ context.Context, key, _ string, body []byte) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.exists {
		return "", fmt.Errorf("%w: scripted", objectstore.ErrObjectExists)
	}
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[key] = body
	return "etag-1", nil
}

// A day already in the store is a success with no second receipt: the first
// archiving wrote one, and a receipt whose digest described bytes this run did
// not write would be a lie. This is the second idempotency guard — the one that
// holds after the job record has been reaped.
func TestArchivingADayAlreadyInTheStoreSucceedsWithoutASecondReceipt(t *testing.T) {
	store := &scriptedArchiveStore{exists: true}
	emitter := &recordingAuditEmitter{}
	svc := &Service{
		auditArchive: AuditArchive{Prefix: "audit/archive", Store: store},
		store:        &archiveQueryStore{},
		audit:        emitter,
		auditTx:      emitter,
	}

	require.NoError(t, svc.archiveAuditDay(context.Background(), "2026-11-02"))
	require.Empty(t, emitter.entries)
}

// The receipt carries the digest of exactly the bytes that were written, so
// "is the object in the bucket the object we wrote" is answerable from the
// trail and not from the bucket alone.
func TestArchivingADayWritesItAndReceiptsItsDigest(t *testing.T) {
	store := &scriptedArchiveStore{}
	emitter := &recordingAuditEmitter{}
	svc := &Service{
		auditArchive: AuditArchive{Prefix: "audit/archive", Store: store},
		store:        &archiveQueryStore{},
		audit:        emitter,
		auditTx:      emitter,
	}

	require.NoError(t, svc.archiveAuditDay(context.Background(), "2026-11-02"))

	key := "audit/archive/2026/11/02/audit-2026-11-02.json"
	body, written := store.objects[key]
	require.True(t, written, "the day's object is written under its own key")
	// A day with no events is still archived, as an empty array: the absence of
	// an object must mean "not archived" and nothing else.
	var entries []auditExportEntry
	require.NoError(t, json.Unmarshal(body, &entries))
	require.Empty(t, entries)

	require.Len(t, emitter.entries, 1)
	receipt := emitter.entries[0]
	require.Equal(t, EventAuditArchiveExported, receipt.EventType)
	require.Equal(t, ActorTypeSystem, receipt.ActorType)
	require.Equal(t, key, receipt.Payload["object_key"])
	require.Equal(t, "2026-11-02", receipt.Payload["day"])
	digest := sha256.Sum256(body)
	require.Equal(t, "sha256:"+hex.EncodeToString(digest[:]), receipt.Payload["digest"])
	require.Equal(t, len(body), receipt.Payload["bytes"])
	require.Equal(t, 0, receipt.Payload["events"])
	// Every declared field, and only declared fields: an undeclared payload
	// field is dropped on the way to the store, so the receipt would lose it.
	require.NoError(t, ValidatePayload(EventAuditArchiveExported, receipt.Payload))
}

// A store refusal is retryable — it may accept in a minute, and the attempt
// budget decides when to give up — so it is returned as an ordinary error and
// no receipt is written for an object that is not there.
func TestAStoreRefusalIsRetryableAndWritesNoReceipt(t *testing.T) {
	emitter := &recordingAuditEmitter{}
	svc := &Service{
		auditArchive: AuditArchive{Prefix: "audit/archive", Store: &scriptedArchiveStore{
			err: fmt.Errorf("the store is unreachable"),
		}},
		store:   &archiveQueryStore{},
		audit:   emitter,
		auditTx: emitter,
	}

	err := svc.archiveAuditDay(context.Background(), "2026-11-02")
	require.ErrorContains(t, err, "write the audit archive object")
	require.Empty(t, emitter.entries)
}

// A day enqueued by a runtime that had a destination, run by one that no longer
// does, is a permanent failure: there is nowhere to write and no amount of
// retrying creates one.
func TestArchivingWithoutADestinationIsPermanent(t *testing.T) {
	svc := &Service{store: &archiveQueryStore{}}
	err := svc.archiveAuditDay(context.Background(), "2026-11-02")
	require.ErrorContains(t, err, "audit_archive.not_configured")
	var processing *jobs.ProcessingError
	require.ErrorAs(t, err, &processing)
	require.False(t, processing.Retryable, "there is nowhere to write, so retrying creates nothing")
}

// archiveQueryStore answers the control-plane wrap and an empty audit log.
type archiveQueryStore struct {
	Store
}

func (s *archiveQueryStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *archiveQueryStore) QueryAuditLog(context.Context, AuditQuery) ([]AuditEntry, string, int32, error) {
	return nil, "", 0, nil
}

// recordingAuditEmitter captures what the archive receipted.
type recordingAuditEmitter struct {
	entries []AuditEntry
}

func (e *recordingAuditEmitter) Emit(_ context.Context, entry AuditEntry) {
	e.entries = append(e.entries, entry)
}

func (e *recordingAuditEmitter) EmitTx(_ context.Context, entry AuditEntry) error {
	e.entries = append(e.entries, entry)
	return nil
}

// The store's audit window is inclusive at both ends, so a day's export must
// stop one instant before the next midnight. Without that, an event written at
// exactly 00:00:00.000000 lands in two consecutive days' archives — a
// duplicated row in a trail whose whole value is that it is exact.
func TestADaysWindowDoesNotOverlapTheNext(t *testing.T) {
	store := &windowRecordingStore{}
	svc := &Service{
		auditArchive: AuditArchive{Prefix: "audit/archive", Store: &scriptedArchiveStore{}},
		store:        store,
	}

	_, _, err := svc.auditDayExport(context.Background(), "2026-11-02")
	require.NoError(t, err)
	require.NotNil(t, store.from)
	require.NotNil(t, store.to)
	require.Equal(t, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), store.from.UTC())
	require.Equal(t, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC).Add(-time.Microsecond), store.to.UTC())
	require.True(t, store.to.Before(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
		"the next day's first instant belongs to the next day's archive")
}

// windowRecordingStore captures the window the export asked for.
type windowRecordingStore struct {
	Store
	from, to *time.Time
}

func (s *windowRecordingStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *windowRecordingStore) QueryAuditLog(_ context.Context, q AuditQuery) ([]AuditEntry, string, int32, error) {
	s.from, s.to = q.From, q.To
	return nil, "", 0, nil
}
