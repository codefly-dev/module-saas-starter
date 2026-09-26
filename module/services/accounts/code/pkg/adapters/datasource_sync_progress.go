package adapters

import (
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

var sourceSyncPhases = map[business.DatasourceSyncPhase]gen.SourceSyncPhase{
	business.DatasourceSyncPhaseQueued:    gen.SourceSyncPhase_SOURCE_SYNC_PHASE_QUEUED,
	business.DatasourceSyncPhaseFetching:  gen.SourceSyncPhase_SOURCE_SYNC_PHASE_FETCHING,
	business.DatasourceSyncPhaseCompiled:  gen.SourceSyncPhase_SOURCE_SYNC_PHASE_COMPILED,
	business.DatasourceSyncPhaseHandedOff: gen.SourceSyncPhase_SOURCE_SYNC_PHASE_HANDED_OFF,
	business.DatasourceSyncPhaseDone:      gen.SourceSyncPhase_SOURCE_SYNC_PHASE_DONE,
	business.DatasourceSyncPhaseFailed:    gen.SourceSyncPhase_SOURCE_SYNC_PHASE_FAILED,
}

var sourceSyncTriggers = map[business.DatasourceSyncTrigger]gen.SourceSyncTrigger{
	business.DatasourceSyncTriggerManual:    gen.SourceSyncTrigger_SOURCE_SYNC_TRIGGER_MANUAL,
	business.DatasourceSyncTriggerScheduled: gen.SourceSyncTrigger_SOURCE_SYNC_TRIGGER_SCHEDULED,
	business.DatasourceSyncTriggerWebhook:   gen.SourceSyncTrigger_SOURCE_SYNC_TRIGGER_WEBHOOK,
}

var sourceSyncFailureReasons = map[business.DatasourceSyncFailureReason]gen.SourceSyncFailureReason{
	business.DatasourceSyncFailureRateLimited:     gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_RATE_LIMITED,
	business.DatasourceSyncFailureCredential:      gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_CREDENTIAL,
	business.DatasourceSyncFailureAccessDenied:    gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_ACCESS_DENIED,
	business.DatasourceSyncFailureNotFound:        gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_NOT_FOUND,
	business.DatasourceSyncFailureTooLarge:        gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_TOO_LARGE,
	business.DatasourceSyncFailureHostUnavailable: gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_HOST_UNAVAILABLE,
	business.DatasourceSyncFailureOther:           gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_OTHER,
	business.DatasourceSyncFailureDeliveryFailed:  gen.SourceSyncFailureReason_SOURCE_SYNC_FAILURE_REASON_DELIVERY_FAILED,
}

// sourceSyncProgressToProto maps a projected sync onto the wire. A value the
// map does not name stays UNSPECIFIED rather than being guessed.
func sourceSyncProgressToProto(progress business.DatasourceSyncProgress) *gen.SourceSyncProgress {
	out := &gen.SourceSyncProgress{
		Phase:       sourceSyncPhases[progress.Phase],
		Trigger:     sourceSyncTriggers[progress.Trigger],
		QueuedAt:    optionalTimestamp(&progress.QueuedAt),
		FetchingAt:  optionalTimestamp(progress.FetchingAt),
		CompiledAt:  optionalTimestamp(progress.CompiledAt),
		HandedOffAt: optionalTimestamp(progress.HandedOffAt),
		FinishedAt:  optionalTimestamp(progress.FinishedAt),
		Attempt:     nonNegative(progress.Attempt),
		MaxAttempts: nonNegative(progress.MaxAttempts),
	}
	if changes := progress.Changes; changes != nil {
		out.Changes = &gen.SourceSyncChanges{
			Files:      nonNegative(changes.Files),
			Added:      nonNegative(changes.Added),
			Modified:   nonNegative(changes.Modified),
			Deleted:    nonNegative(changes.Deleted),
			SplitKnown: changes.SplitKnown,
			Snapshot:   changes.Snapshot,
			Commit:     changes.Commit,
		}
	}
	if failure := progress.Failure; failure != nil {
		out.Failure = &gen.SourceSyncFailure{
			Reason:   sourceSyncFailureReasons[failure.Reason],
			Code:     failure.Code,
			Message:  failure.Message,
			Retrying: failure.Retrying,
			RetryAt:  optionalTimestamp(failure.RetryAt),
		}
	}
	return out
}

func optionalTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

func nonNegative(n int) uint32 {
	if n < 0 {
		return 0
	}
	return uint32(n)
}
