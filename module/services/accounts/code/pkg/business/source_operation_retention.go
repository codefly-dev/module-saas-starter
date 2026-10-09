package business

import (
	"bytes"
	"context"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/runnable"
	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const SourceReceiptRetentionMethod = "/saas.accounts.v1.DatasourceService/PruneSourceOperationReceipts"
const sourceReceiptRetentionOperation = "_receipt_retention"
const SourceReceiptLookupGrace = 24 * time.Hour

type SourceReceiptRetentionStore interface {
	PruneSourceOperationReceipts(context.Context, string, string, time.Time) (int64, error)
}

// SourceReceiptRetentionWindow follows the declared execution deadline rather
// than duplicating its timeout in a cleanup configuration.
func SourceReceiptRetentionWindow() (time.Duration, error) {
	method := gen.File_saas_accounts_v1_datasource_proto.Services().ByName("DatasourceService").Methods().ByName("InvokeSourceOperation")
	spec, err := runnable.OperationFromMethod(method)
	if err != nil {
		return 0, err
	}
	return spec.TotalTimeout + SourceReceiptLookupGrace, nil
}

// SourceReceiptRetentionAuthority is checked before receipt replay and again
// after acquiring the SDK serialization hold. A runtime must present the same
// installed source scope as an invocation. Shared sources require an organization
// admin; a personal source can be maintained only by its owning member.
func (s *Service) SourceReceiptRetentionAuthority(ctx context.Context, actor, org, source, effectID string, lookup bool) error {
	tenant, _, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok || tenant != org {
		return status.Error(codes.PermissionDenied, "source tenant mismatch")
	}
	return s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		member, err := s.store.GetOrgMembership(ctx, org, actor)
		if err != nil {
			return operationUnavailable()
		}
		if member == nil {
			return status.Error(codes.PermissionDenied, "organization membership required")
		}
		action := "invoke"
		if lookup {
			action = "read"
		}
		src, err := s.sourceOperationAccess(ctx, actor, org, source, action)
		if err != nil {
			return err
		}
		if src.PersonalOwnerUserID != actor && !IsOrgAdminRole(orgRoleToString(member.Role)) {
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
		if attempt != nil && (attempt.ActorID != actor || attempt.SourceID != source || attempt.Operation != sourceReceiptRetentionOperation) {
			return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
		}
		return nil
	})
}

// PruneSourceOperationReceipts is a host operation scheduled by the runtime.
// It removes large expired response bodies, never the compact effect markers:
// forgetting a mutation's last marker would permit a duplicate remote effect.
// Deletion, audit and this operation's receipt commit in the same transaction.
func (s *Service) PruneSourceOperationReceipts(ctx context.Context, actor, org, source string, commit func(context.Context, int64) error) error {
	effect, ok := receipts.EffectFromContext(ctx)
	if !ok || effect.Tenant != org || effect.Method != SourceReceiptRetentionMethod || commit == nil {
		return operationRefused("SOURCE_EFFECT_REQUIRED")
	}
	if err := s.SourceReceiptRetentionAuthority(ctx, actor, org, source, effect.ID, false); err != nil {
		return err
	}
	retention, err := SourceReceiptRetentionWindow()
	if err != nil {
		return operationUnavailable()
	}
	store, ok := s.store.(SourceReceiptRetentionStore)
	if !ok {
		return operationUnavailable()
	}
	err = s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		attempts := s.store.(SourceOperationAttemptStore)
		prior, err := attempts.GetSourceOperationAttempt(ctx, org, effect.ID)
		if err != nil {
			return operationUnavailable()
		}
		if prior != nil {
			if !bytes.Equal(prior.RequestDigest, effect.RequestDigest) {
				return operationRefused("SOURCE_EFFECT_REUSED")
			}
			// A previous cleanup whose small response expired is safe to repeat.
		} else {
			created, err := attempts.CreateSourceOperationAttempt(ctx, SourceOperationAttempt{OrgID: org, EffectID: effect.ID, ActorID: actor, SourceID: source, Operation: sourceReceiptRetentionOperation, DeclarationDigest: SourceReceiptRetentionMethod, RequestDigest: effect.RequestDigest})
			if err != nil || !created {
				return operationUnavailable()
			}
		}
		removed, err := store.PruneSourceOperationReceipts(ctx, org, source, time.Now().UTC().Add(-retention))
		if err != nil {
			return operationUnavailable()
		}
		if err := s.emitTx(ctx, actor, "user", EventDatasourceReceiptsPruned, "datasource", source, org, sourceReceiptRetentionAudit(effect.ID, removed)); err != nil {
			return err
		}
		return commit(ctx, removed)
	})
	if err != nil && status.Code(err) == codes.Unknown {
		return operationUnavailable()
	}
	return err
}

func sourceReceiptRetentionAudit(effect string, removed int64) map[string]any {
	return map[string]any{"effect_id": effect, "receipts": removed}
}
