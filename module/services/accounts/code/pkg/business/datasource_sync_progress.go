package business

import (
	"context"
	"strconv"
	"strings"
	"time"

	"accounts/pkg/datasource/connector"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/core/wool"
)

// A sync's progress is projected from the durable records the host already
// keeps of it — the sync job on DatasourceDeliveryQueue and the change set it
// handed to the consuming module's queue — never from elapsed time and never
// from per-file host events. Each phase is stamped by the record that proves it:
//
//	queued     the sync job's created_at
//	fetching   the latest attempt's lease (last_attempt_at)
//	compiled   the first hand-off job's created_at, with the change-set counts
//	handed off the sync job's completion, once hand-off jobs exist
//	done       the last hand-off job taken by the module (or, with nothing to
//	           hand off, the sync job's completion)
//	failed     the sync job's dead-letter, or a hand-off job's
const (
	// datasourceWebhookPushSource must equal the receiver's
	// datasource.GitHubWebhookSource: a per-source webhook delivery is one of
	// the sync jobs a source's progress reads.
	datasourceWebhookPushSource = "github.webhook"

	// The snapshot hand-off job carries its change-set counts, so a progress
	// read need not open the manifest. An incremental change set needs none: it
	// is one hand-off job per file, each typed by github.change_type.
	attrChangesFiles      = "datasource.changes.files"
	attrChangesAdded      = "datasource.changes.added"
	attrChangesModified   = "datasource.changes.modified"
	attrChangesDeleted    = "datasource.changes.deleted"
	attrChangesSplitKnown = "datasource.changes.split_known"
)

// DatasourceSyncSources are the job sources a source's sync can come from: a
// reconcile (connect, "Sync now", the periodic sweep), a per-source webhook
// delivery, or a GitHub App delivery fanned out to the source.
var DatasourceSyncSources = []string{DatasourceReconcileSource, datasourceWebhookPushSource, datasourceAppPushSource}

// DatasourceSyncTopics are the topics of those sync jobs.
var DatasourceSyncTopics = []string{DatasourceReconcileTopic, datasourcePushTopic}

// DatasourceChangeSetFileTopic is the topic of one changed file of an
// incremental change set.
const DatasourceChangeSetFileTopic = datasourceSyncTopic

// DatasourceHandoffTopics are the topics of the change-set jobs a sync hands to
// the consuming module: a full snapshot manifest, or one job per changed file.
var DatasourceHandoffTopics = []string{DatasourceSnapshotTopic, DatasourceChangeSetFileTopic}

// DatasourceSyncPhase is the stage the host has reached in one sync.
type DatasourceSyncPhase int

const (
	DatasourceSyncPhaseUnknown DatasourceSyncPhase = iota
	DatasourceSyncPhaseQueued
	DatasourceSyncPhaseFetching
	DatasourceSyncPhaseCompiled
	DatasourceSyncPhaseHandedOff
	DatasourceSyncPhaseDone
	DatasourceSyncPhaseFailed
)

// DatasourceSyncTrigger is what started a sync.
type DatasourceSyncTrigger int

const (
	DatasourceSyncTriggerUnknown DatasourceSyncTrigger = iota
	DatasourceSyncTriggerManual
	DatasourceSyncTriggerScheduled
	DatasourceSyncTriggerWebhook
)

// DatasourceSyncFailureReason classifies why a sync waits or failed.
type DatasourceSyncFailureReason int

const (
	DatasourceSyncFailureUnknown DatasourceSyncFailureReason = iota
	DatasourceSyncFailureRateLimited
	DatasourceSyncFailureCredential
	DatasourceSyncFailureAccessDenied
	DatasourceSyncFailureNotFound
	DatasourceSyncFailureTooLarge
	DatasourceSyncFailureHostUnavailable
	DatasourceSyncFailureOther
	DatasourceSyncFailureDeliveryFailed
)

// DatasourceSyncRecord is the sync job's own durable lifecycle.
type DatasourceSyncRecord struct {
	JobID         string
	Topic         string
	ReconcileMode string
	State         jobsv1.JobState
	Attempt       int
	MaxAttempts   int
	CreatedAt     time.Time
	LastAttemptAt *time.Time
	CompletedAt   *time.Time
	DeadAt        *time.Time
	AvailableAt   *time.Time
	ErrorCode     string
	ErrorMessage  string
}

// DatasourceSyncHandoff aggregates the change-set jobs one sync handed to the
// consuming module's queue. Counts are raw attribute values as stored, parsed
// by the projection.
type DatasourceSyncHandoff struct {
	Jobs        int
	Succeeded   int
	Dead        int
	FirstAt     *time.Time
	LastDoneAt  *time.Time
	FirstDeadAt *time.Time
	Snapshot    bool
	Commit      string

	// Incremental: one job per changed file, counted by change type.
	Added, Modified, Deleted int

	// Snapshot: the counts the compiler stamped on the manifest job.
	SnapshotFiles, SnapshotAdded, SnapshotModified, SnapshotDeleted, SnapshotSplitKnown string
}

// DatasourceSyncChanges counts a compiled change set.
type DatasourceSyncChanges struct {
	Files, Added, Modified, Deleted int
	SplitKnown                      bool
	Snapshot                        bool
	Commit                          string
}

// DatasourceSyncFailure is why a sync waits to retry, or failed.
type DatasourceSyncFailure struct {
	Reason   DatasourceSyncFailureReason
	Code     string
	Message  string
	Retrying bool
	RetryAt  *time.Time
}

// DatasourceSyncProgress is one sync's phases.
type DatasourceSyncProgress struct {
	Phase       DatasourceSyncPhase
	Trigger     DatasourceSyncTrigger
	QueuedAt    time.Time
	FetchingAt  *time.Time
	CompiledAt  *time.Time
	HandedOffAt *time.Time
	FinishedAt  *time.Time
	Changes     *DatasourceSyncChanges
	Failure     *DatasourceSyncFailure
	Attempt     int
	MaxAttempts int
}

// datasourceDeliveryFailedMessage is the host's own sentence for a hand-off job
// the consuming module dead-lettered. The module's failure text is the
// module's, and the module reports it on its own surface.
const datasourceDeliveryFailedMessage = "The consuming module could not take part of this change set and stopped retrying it. Its own view of the source says why."

// ProjectDatasourceSyncProgress derives a sync's phases from its durable
// records. It is pure so every phase transition is testable without a database.
func ProjectDatasourceSyncProgress(record DatasourceSyncRecord, handoff DatasourceSyncHandoff) DatasourceSyncProgress {
	progress := DatasourceSyncProgress{
		Trigger:     datasourceSyncTrigger(record),
		QueuedAt:    record.CreatedAt,
		FetchingAt:  record.LastAttemptAt,
		Attempt:     record.Attempt,
		MaxAttempts: record.MaxAttempts,
	}
	if handoff.Jobs > 0 {
		progress.CompiledAt = handoff.FirstAt
		progress.Changes = handoffChanges(handoff)
	}
	if record.ErrorCode != "" && (record.State == jobsv1.JobState_JOB_STATE_RETRYING ||
		record.State == jobsv1.JobState_JOB_STATE_DEAD_LETTER) {
		failure := &DatasourceSyncFailure{
			Reason:   DatasourceSyncFailureReasonForCode(record.ErrorCode),
			Code:     record.ErrorCode,
			Message:  record.ErrorMessage,
			Retrying: record.State == jobsv1.JobState_JOB_STATE_RETRYING,
		}
		if failure.Retrying {
			failure.RetryAt = record.AvailableAt
		}
		progress.Failure = failure
	}

	switch record.State {
	case jobsv1.JobState_JOB_STATE_PENDING, jobsv1.JobState_JOB_STATE_RETRYING:
		progress.Phase = DatasourceSyncPhaseQueued
	case jobsv1.JobState_JOB_STATE_PROCESSING:
		progress.Phase = DatasourceSyncPhaseFetching
		if handoff.Jobs > 0 {
			progress.Phase = DatasourceSyncPhaseCompiled
		}
	case jobsv1.JobState_JOB_STATE_DEAD_LETTER, jobsv1.JobState_JOB_STATE_CANCELED:
		progress.Phase = DatasourceSyncPhaseFailed
		progress.FinishedAt = firstTime(record.DeadAt, record.CompletedAt)
		if progress.Failure == nil {
			progress.Failure = &DatasourceSyncFailure{Reason: DatasourceSyncFailureOther, Code: "datasource.sync_canceled", Message: "This sync was canceled before it finished."}
		}
	case jobsv1.JobState_JOB_STATE_SUCCEEDED:
		switch {
		case handoff.Jobs == 0:
			// Nothing to hand off: the head had not moved, or the delivery was
			// for another branch or already ingested.
			progress.Phase = DatasourceSyncPhaseDone
			progress.FinishedAt = record.CompletedAt
		case handoff.Succeeded >= handoff.Jobs:
			progress.Phase = DatasourceSyncPhaseDone
			progress.HandedOffAt = record.CompletedAt
			progress.FinishedAt = firstTime(handoff.LastDoneAt, record.CompletedAt)
		default:
			progress.Phase = DatasourceSyncPhaseHandedOff
			progress.HandedOffAt = record.CompletedAt
		}
	}
	if handoff.Dead > 0 && progress.Phase != DatasourceSyncPhaseFailed {
		progress.Phase = DatasourceSyncPhaseFailed
		progress.FinishedAt = handoff.FirstDeadAt
		progress.Failure = &DatasourceSyncFailure{Reason: DatasourceSyncFailureDeliveryFailed, Code: "datasource.delivery_dead_lettered", Message: datasourceDeliveryFailedMessage}
	}
	return progress
}

func datasourceSyncTrigger(record DatasourceSyncRecord) DatasourceSyncTrigger {
	switch {
	case record.Topic == datasourcePushTopic:
		return DatasourceSyncTriggerWebhook
	case record.ReconcileMode == reconcileModeForce:
		return DatasourceSyncTriggerManual
	case record.Topic == DatasourceReconcileTopic:
		return DatasourceSyncTriggerScheduled
	}
	return DatasourceSyncTriggerUnknown
}

func handoffChanges(handoff DatasourceSyncHandoff) *DatasourceSyncChanges {
	changes := &DatasourceSyncChanges{Snapshot: handoff.Snapshot, Commit: handoff.Commit}
	if !handoff.Snapshot {
		changes.Files = handoff.Jobs
		changes.Added, changes.Modified, changes.Deleted = handoff.Added, handoff.Modified, handoff.Deleted
		changes.SplitKnown = true
		return changes
	}
	// A snapshot handed off before the compiler stamped counts carries none;
	// report the snapshot with nothing known rather than a zero that reads as
	// "no files".
	files, err := strconv.Atoi(handoff.SnapshotFiles)
	if err != nil || files < 0 {
		return changes
	}
	changes.Files = files
	if handoff.SnapshotSplitKnown != "true" {
		return changes
	}
	added, errA := strconv.Atoi(handoff.SnapshotAdded)
	modified, errM := strconv.Atoi(handoff.SnapshotModified)
	deleted, errD := strconv.Atoi(handoff.SnapshotDeleted)
	if errA != nil || errM != nil || errD != nil {
		return changes
	}
	changes.Added, changes.Modified, changes.Deleted, changes.SplitKnown = added, modified, deleted, true
	return changes
}

// DatasourceSyncFailureReasonForCode maps the host's stable failure codes onto
// the reasons a client acts on. An unknown code is Other, never a guess.
func DatasourceSyncFailureReasonForCode(code string) DatasourceSyncFailureReason {
	switch code {
	case "datasource.github_rate_limited", "datasource.github_unauthenticated_rate_limited":
		return DatasourceSyncFailureRateLimited
	case "datasource.credential_unreadable", "datasource.github_unauthorized", "datasource.validation_failed":
		return DatasourceSyncFailureCredential
	case "datasource.github_access_or_rate_limit", "datasource.github_app_access_denied", "datasource.github_app_scope_denied":
		return DatasourceSyncFailureAccessDenied
	case "datasource.github_not_found":
		return DatasourceSyncFailureNotFound
	case "datasource.repository_too_large", "datasource.too_many_files":
		return DatasourceSyncFailureTooLarge
	case "datasource.git_unavailable", "datasource.credential_service_unavailable", "datasource.credential_store_unavailable",
		"datasource.github_app_token_unavailable", "datasource.github_app_unconfigured":
		return DatasourceSyncFailureHostUnavailable
	}
	return DatasourceSyncFailureOther
}

// startFirstGitHubSync enqueues a new GitHub source's first sync the moment it
// is connected, so its progress is visible from the first second instead of
// waiting for the periodic reconcile (its first run is a full interval away).
// It is a forced reconcile — the same job "Sync now" enqueues — so it reports as
// a manual sync. The source is already committed: a failure to enqueue is
// logged, and the periodic reconcile or "Sync now" still syncs it.
func (s *Service) startFirstGitHubSync(ctx context.Context, source *DatasourceSource) {
	if s.datasourceJobs == nil {
		return
	}
	if err := s.enqueueReconcile(ctx, source, reconcileModeForce); err != nil {
		wool.Get(ctx).In("startFirstGitHubSync").Warn("enqueue first sync failed; the periodic reconcile will sync the source",
			wool.Field("source", source.ID), wool.ErrField(err))
	}
}

func firstTime(times ...*time.Time) *time.Time {
	for _, t := range times {
		if t != nil {
			return t
		}
	}
	return nil
}

// snapshotChangeAttributes counts a snapshot against the commit the host last
// handed off for the source, and returns the counts as the attributes the
// snapshot job carries. No previous commit makes every file an addition; the
// same commit makes a snapshot with no changes (a forced re-sync). Otherwise
// the split is the connector's own change set from the previous commit; when
// the connector cannot diff from it — the previous commit is gone after a force
// push, the histories diverged, or the head moved past the snapshot meanwhile —
// the file count stands alone and the split is reported unknown.
func (s *Service) snapshotChangeAttributes(ctx context.Context, conn connector.FilesConnector, src connector.Source, source *DatasourceSource, commit string, files int) map[string]string {
	attrs := map[string]string{attrChangesFiles: strconv.Itoa(files), attrChangesSplitKnown: "false"}
	known := func(added, modified, deleted int) map[string]string {
		attrs[attrChangesAdded] = strconv.Itoa(added)
		attrs[attrChangesModified] = strconv.Itoa(modified)
		attrs[attrChangesDeleted] = strconv.Itoa(deleted)
		attrs[attrChangesSplitKnown] = "true"
		return attrs
	}
	previous := strings.TrimSpace(source.LastIngestedCommit)
	switch previous {
	case "":
		return known(files, 0, 0)
	case commit:
		return known(0, 0, 0)
	}
	cs, err := conn.Changes(ctx, src, previous)
	if err != nil || cs.To != commit {
		return attrs
	}
	var added, modified, deleted int
	for _, op := range changeOpsFrom(cs.Changes) {
		switch op.changeType {
		case changeTypeAdded:
			added++
		case changeTypeRemoved:
			deleted++
		default:
			modified++
		}
	}
	return known(added, modified, deleted)
}

// withAttributes adds extra's entries to base and returns it. The change-set
// counts use their own datasource.changes.* keys, so nothing is overwritten.
func withAttributes(base, extra map[string]string) map[string]string {
	for k, v := range extra {
		base[k] = v
	}
	return base
}
