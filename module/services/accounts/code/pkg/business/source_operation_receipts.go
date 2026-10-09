package business

import (
	"context"
	"errors"

	"accounts/pkg/auth"

	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/protobuf/proto"
)

const SourceOperationAudience = "saas-datasource"

// SourceOperationReceiptWriter binds the SDK receipt to the host transaction.
type SourceOperationReceiptWriter interface {
	RecordSourceOperationReceipt(context.Context, receipts.Store, proto.Message) error
}

// ConfigureSourceOperationReceipts is called once, before listeners are served.
func (s *Service) ConfigureSourceOperationReceipts(store receipts.Store) error {
	if _, ok := s.store.(SourceOperationReceiptWriter); !ok {
		return errors.New("source receipt transaction writer unavailable")
	}
	guard, err := receipts.New(receipts.Options{Store: sourceAuthorityReceiptStore{Store: store}, Tenant: func(ctx context.Context) (string, error) {
		tenant, _, ok := auth.VerifiedDatabaseIdentity(ctx)
		if !ok || tenant == "" {
			return "", errors.New("source receipt tenant unavailable")
		}
		return tenant, nil
	}})
	if err != nil {
		return err
	}
	if !guard.IsOperation(SourceOperationMethod) || !guard.IsOperation(SourceReceiptRetentionMethod) {
		return errors.New("source operation marking unavailable")
	}
	s.sourceReceiptStore, s.sourceReceiptGuard = store, guard
	return nil
}
func (s *Service) SourceOperationReceipts() (receipts.Store, *receipts.Interceptor) {
	return s.sourceReceiptStore, s.sourceReceiptGuard
}
func (s *Service) RecordSourceOperationReceipt(ctx context.Context, response proto.Message) error {
	writer, ok := s.store.(SourceOperationReceiptWriter)
	if !ok || s.sourceReceiptStore == nil {
		return operationUnavailable()
	}
	return writer.RecordSourceOperationReceipt(ctx, s.sourceReceiptStore, response)
}

// SourceOperationAttemptForActor locates recovery metadata inside the verified
// tenant. Authority on the live source is checked before returning any output.
func (s *Service) SourceOperationAttemptForActor(ctx context.Context, actor, org, effect string) (*SourceOperationAttempt, error) {
	store, ok := s.store.(SourceOperationAttemptStore)
	if !ok {
		return nil, operationUnavailable()
	}
	var attempt *SourceOperationAttempt
	err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		var err error
		attempt, err = store.GetSourceOperationAttempt(ctx, org, effect)
		if err != nil {
			return operationUnavailable()
		}
		if attempt != nil && attempt.ActorID != actor {
			return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
		}
		return nil
	})
	return attempt, err
}

// A request may wait behind another holder of its effect id. Recheck authority
// after that wait, immediately before the SDK reads or replays a receipt.
type sourceOperationRecheckKey struct{}

func WithSourceOperationRecheck(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, sourceOperationRecheckKey{}, check)
}

type sourceAuthorityReceiptStore struct{ receipts.Store }

func (s sourceAuthorityReceiptStore) Serialize(ctx context.Context, tenant, effect, method string) (receipts.Held, error) {
	held, err := s.Store.Serialize(ctx, tenant, effect, method)
	if err != nil {
		return nil, err
	}
	check, ok := ctx.Value(sourceOperationRecheckKey{}).(func(context.Context) error)
	if !ok {
		held.Release()
		return nil, operationRefused("SOURCE_AUTHORITY_REQUIRED")
	}
	if err = check(ctx); err != nil {
		held.Release()
		return nil, err
	}
	return held, nil
}
