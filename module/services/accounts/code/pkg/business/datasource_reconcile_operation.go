package business

import (
	"bytes"
	"context"
	"strings"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReconcileSourceMethod is the host operation one occurrence of a declared
// reconcile schedule admits. It is an OPERATION and not a sweep: it names
// exactly one source, it carries the effect identity its caller chose, and its
// receipt is read back rather than inferred.
const ReconcileSourceMethod = "/saas.accounts.v1.DatasourceService/ReconcileSource"

// reconcileSourceOperation is the operation name the effect's attempt row
// records, beside the two callable-source operations. It is not a declared
// source operation and never resolves to one: the leading underscore is how
// this host's own operations are spelled in that column.
const reconcileSourceOperation = "_reconcile"

// ReconcileDatasourceSource performs one reconcile pass over one source: the
// work a declared schedule's occurrence admits, and the same work a person
// asks for by hand.
//
// It does no provider work itself. GitHub is handed a conditional reconcile —
// the change-set compiler resolves the head and snapshots only if it moved —
// and a pull provider the full sync its connector has, which is the only
// engine those connectors have. That is deliberate and it is what the sweep
// did too: an occurrence that waited on an unreachable provider would hold its
// release's capacity for the provider's timeout rather than for the handover.
//
// The pass is bound to its effect identity in three places, and each has a
// reason. The enqueue is keyed by it, so two attempts under one identity are
// one job rather than two full re-sends of a source. The attempt row binds it
// to the actor and the source, so an identity cannot be re-presented for
// another source. And the receipt commits with the audit record in one
// transaction, so an operator reading the trail and a caller recovering the
// outcome cannot disagree.
func (s *Service) ReconcileDatasourceSource(ctx context.Context, actor, org, sourceID string, commit func(context.Context, string) error) error {
	effect, ok := receipts.EffectFromContext(ctx)
	if !ok || effect.Tenant != org || effect.Method != ReconcileSourceMethod || commit == nil {
		return operationRefused("SOURCE_EFFECT_REQUIRED")
	}
	source, err := s.GetDatasourceSource(ctx, org, strings.TrimSpace(sourceID))
	if err != nil {
		return err
	}
	if source.PersonalOwnerUserID != "" && source.PersonalOwnerUserID != actor {
		return status.Error(codes.PermissionDenied, "source is personal to another member")
	}
	if source.API != nil && source.API.OperationsOnly {
		return status.Error(codes.FailedPrecondition, "this source accepts declared operations only")
	}
	if source.Status != DatasourceStatusActive {
		// A parked source declares no trigger, so no occurrence should reach
		// here; a person's manual pass still can. Refused rather than silently
		// doing nothing: the reason a source is parked is an operator's to
		// resolve, and a pass that reported success would hide it.
		return status.Errorf(codes.FailedPrecondition, "source is %s and is not reconciled", source.Status)
	}
	if s.datasourceJobs == nil {
		return operationUnavailable()
	}
	attempts, ok := s.store.(SourceOperationAttemptStore)
	if !ok {
		return operationUnavailable()
	}
	// The enqueue comes before the record, and the record is what makes the
	// pass recoverable. That order is safe only because the enqueue is keyed by
	// the effect: a crash between the two leaves one job that a retry under the
	// same identity finds already enqueued, rather than a second re-send.
	jobID, err := s.enqueueReconcileOccurrence(ctx, source, effect.ID)
	if err != nil {
		return err
	}
	err = s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		prior, err := attempts.GetSourceOperationAttempt(ctx, org, effect.ID)
		if err != nil {
			return operationUnavailable()
		}
		switch {
		case prior == nil:
			created, err := attempts.CreateSourceOperationAttempt(ctx, SourceOperationAttempt{
				OrgID: org, EffectID: effect.ID, ActorID: actor, SourceID: source.ID,
				Operation: reconcileSourceOperation, DeclarationDigest: ReconcileSourceMethod,
				RequestDigest: effect.RequestDigest,
			})
			if err != nil || !created {
				return operationUnavailable()
			}
		case prior.ActorID != actor || prior.SourceID != source.ID || prior.Operation != reconcileSourceOperation:
			return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
		case !bytes.Equal(prior.RequestDigest, effect.RequestDigest):
			return operationRefused("SOURCE_EFFECT_REUSED")
		}
		return commit(ctx, jobID)
	})
	if err != nil {
		if status.Code(err) == codes.Unknown {
			return operationUnavailable()
		}
		return err
	}
	// The observation is emitted OUTSIDE the transaction above, and that is the
	// durability rule rather than an oversight: "a sync was requested" observes
	// a handover that has already happened, so rolling it back with the write
	// it observes would lose the record of work that is still going to run.
	s.emit(ctx, actor, "user", EventDatasourceSourceSynced, "datasource", source.ID, org,
		map[string]any{"job_id": jobID, "repo": source.Repo})
	// The schedule's own projection moves with the pass, not with a clock this
	// host runs: the stored instant is what the park and restore paths read as
	// "this source is on its schedule", and a pass that left it alone would let
	// it drift arbitrarily far into the past.
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.BumpDatasourceReconcile(ctx, source.ID)
	})
}

// enqueueReconcileOccurrence hands one pass to the engine the source's provider
// actually syncs on, keyed by the effect so a retry is the same job.
//
// The two paths are the two engines, and nothing else differs: GitHub is
// compiled by the change-set worker off the delivery queue; every other
// provider is a full pull performed by the sync worker. Both carry the
// occurrence's provenance, so an operator reading the queue can tell a
// declared schedule's pass from a person's.
func (s *Service) enqueueReconcileOccurrence(ctx context.Context, source *DatasourceSource, effectID string) (string, error) {
	// The key names the organization and the source as well as the effect
	// (issue #1112, m2). An effect identity is the CALLER's to choose and the
	// GitHub job's scope is Global, so a key of the effect alone put every
	// tenant's reconciles in one namespace: a caller in one organization could
	// pick an identity already pending for a source in another and have the
	// platform collapse the two, suppressing that organization's reconcile
	// with no refusal anywhere. Both idempotency guarantees the operation
	// needs are unaffected — two attempts of one occurrence still agree on all
	// three parts, and the next occurrence still differs in the effect.
	key := "datasource.reconcile:" + source.OrgID + ":" + source.ID + ":" + effectID
	if source.Provider == DatasourceProviderGitHub {
		return s.enqueueJobID(ctx, &jobsv1.NewJob{
			Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			Queue:          DatasourceDeliveryQueue,
			Topic:          DatasourceReconcileTopic,
			Source:         DatasourceReconcileSource,
			Ordering:       DatasourceDeliveryOrderingKey(source.ID),
			IdempotencyKey: key,
			SchemaVersion:  datasourceReconcileSchemaVersion,
			Payload:        datasourceRequestBody(),
			ContentType:    datasourceRequestContentType,
			MaxAttempts:    datasourceDeliveryMaxAttempts,
			Attributes: map[string]string{
				attrSourceID:      source.ID,
				attrOrgID:         source.OrgID,
				attrBoundaryID:    source.BoundaryNodeID,
				attrReconcileMode: reconcileModeConditional,
			},
		})
	}
	return s.enqueueJobID(ctx, &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: source.OrgID}},
		Queue:          DatasourceSyncRequestQueue,
		Topic:          datasourceSyncRequestTopic,
		Source:         DatasourceScheduledSyncSource,
		Ordering:       DatasourceSyncOrderingKey(source.ID),
		IdempotencyKey: key,
		SchemaVersion:  datasourceSyncRequestSchemaVersion,
		Payload:        datasourceRequestBody(),
		ContentType:    datasourceRequestContentType,
		MaxAttempts:    datasourceSyncRequestMaxAttempts,
		Attributes:     map[string]string{attrSourceID: source.ID},
	})
}

// enqueueJobID enqueues one job and returns its durable id, which is what a
// caller traces the pass by.
func (s *Service) enqueueJobID(ctx context.Context, job *jobsv1.NewJob) (string, error) {
	response, err := s.datasourceJobs.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{Job: job})
	if err != nil {
		return "", err
	}
	return response.GetJobId(), nil
}

// ReconcileSourceAuthority is checked before a receipt replay and again after
// the SDK's serialization hold, exactly as the other host operations are: a
// caller that waited behind another holder of its effect id must be authorized
// against current facts, not against the facts of the call that queued.
func (s *Service) ReconcileSourceAuthority(ctx context.Context, actor, org, sourceID, effectID string, lookup bool) error {
	return s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		member, err := s.store.GetOrgMembership(ctx, org, actor)
		if err != nil {
			return operationUnavailable()
		}
		if member == nil {
			return status.Error(codes.PermissionDenied, "organization membership required")
		}
		source, err := s.store.GetDatasourceSource(ctx, org, sourceID)
		if err != nil {
			return operationUnavailable()
		}
		if source == nil {
			return status.Error(codes.NotFound, "datasource source not found")
		}
		// A shared source is an organization administrator's to reconcile; a
		// personal one is its owner's. The same division the rest of this
		// surface makes, and the same one a delegation re-checks: the person a
		// reference borrows authority from holds exactly this role.
		if source.PersonalOwnerUserID != actor && !IsOrgAdminRole(orgRoleToString(member.Role)) {
			return status.Error(codes.PermissionDenied, "source owner or organization administrator required")
		}
		attempts, ok := s.store.(SourceOperationAttemptStore)
		if !ok {
			return operationUnavailable()
		}
		attempt, err := attempts.GetSourceOperationAttempt(ctx, org, effectID)
		if err != nil {
			return operationUnavailable()
		}
		if attempt != nil && (attempt.ActorID != actor || attempt.SourceID != sourceID || attempt.Operation != reconcileSourceOperation) {
			return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
		}
		return nil
	})
}
