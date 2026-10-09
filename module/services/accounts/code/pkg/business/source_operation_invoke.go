package business

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"time"

	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/operations"

	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const SourceOperationMethod = "/saas.accounts.v1.DatasourceService/InvokeSourceOperation"

// SourceOperationAttempt is durable before the outbound request. An attempt
// with no committed SDK receipt means unknown, including after process death.
// Neither request nor provider response is retained in this marker.
type SourceOperationAttempt struct {
	OrgID, EffectID, ActorID, SourceID, Operation, DeclarationDigest string
	RequestDigest                                                    []byte
}

type SourceOperationAttemptStore interface {
	GetSourceOperationAttempt(context.Context, string, string) (*SourceOperationAttempt, error)
	CreateSourceOperationAttempt(context.Context, SourceOperationAttempt) (bool, error)
	DeleteSourceOperationAttempt(context.Context, string, string) error
}

type SourceOperationResult struct {
	Output         []byte
	EffectID       string
	CommittedAt    time.Time
	ProviderStatus int
}

type APIOperationClient interface {
	Do(context.Context, string, string, []byte) (*apisource.Result, error)
}

// SourceOperationAuthority runs before the SDK replay guard as well as before
// dispatch. Invocation fences the current declaration. Lookup recovers retained
// evidence independently of declarations, under the actor's current source read
// authority; deleting or replacing a declaration cannot erase an attempt.
func (s *Service) SourceOperationAuthority(ctx context.Context, actor, org, source, operation, effectID string, lookup bool) (*DatasourceSource, operations.Declaration, error) {
	declarations, ok := s.store.(SourceOperationStore)
	attempts, attemptsOK := s.store.(SourceOperationAttemptStore)
	if !ok || !attemptsOK {
		return nil, operations.Declaration{}, operationUnavailable()
	}
	var src *DatasourceSource
	var declaration operations.Declaration
	declarationRemoved := false
	err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		attempt, err := attempts.GetSourceOperationAttempt(ctx, org, effectID)
		if err != nil {
			return operationUnavailable()
		}
		if attempt != nil && (attempt.ActorID != actor || attempt.SourceID != source || attempt.Operation != operation) {
			return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
		}
		action := "invoke"
		if lookup {
			action = "read"
		}
		src, err = s.sourceOperationAccess(ctx, actor, org, source, action)
		if err != nil {
			if attempt != nil && status.Code(err) == codes.NotFound {
				return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
			}
			return err
		}
		if lookup {
			if attempt == nil {
				return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
			}
			return nil
		}
		if src.Provider != DatasourceProviderAPI || src.API == nil {
			return operationRefused("SOURCE_OPERATIONS_UNAVAILABLE")
		}
		all, err := declarations.ListSourceOperations(ctx, org, source)
		if err != nil {
			return operationUnavailable()
		}
		for _, d := range all {
			if d.Name == operation {
				declaration = d
				break
			}
		}
		if declaration.Name == "" {
			if attempt != nil {
				declarationRemoved = true
				return nil
			}
			return status.Error(codes.NotFound, "source operation not found")
		}
		checked, err := operations.Admit(declaration)
		if err != nil || declaration.Digest == "" {
			return operationRefused("SOURCE_DECLARATION_INVALID")
		}
		declaration = checked
		if attempt != nil && attempt.DeclarationDigest != declaration.Digest {
			return operationRefused("SOURCE_EFFECT_BINDING_CHANGED")
		}
		return nil
	})
	if err == nil && declarationRemoved {
		// Release the authority transaction before the SDK opens its tenant read.
		// Only receipt presence is needed; no response data enters the refusal.
		if s.sourceReceiptStore == nil {
			return src, declaration, operationUnavailable()
		}
		_, committed, lookupErr := s.sourceReceiptStore.Lookup(ctx, org, effectID, SourceOperationMethod)
		if lookupErr != nil {
			return src, declaration, operationUnavailable()
		}
		err = operationDeclarationRemoved(committed)
	}
	return src, declaration, err
}

func operationDeclarationRemoved(committed bool) error {
	receiptStatus := "unknown"
	if committed {
		receiptStatus = "committed"
	}
	return datasourceStatus(codes.FailedPrecondition, "SOURCE_OPERATION_DECLARATION_REMOVED", "source operation declaration was removed; recover the original effect with Lookup", map[string]string{"receipt_status": receiptStatus}, 0)
}

func operationUnavailable() error {
	return datasourceStatus(codes.Unavailable, "SOURCE_OPERATION_UNAVAILABLE", "source operation unavailable", nil, 0)
}
func operationRefused(reason string, providerStatus ...int) error {
	var metadata map[string]string
	if len(providerStatus) == 1 && providerStatus[0] >= 100 && providerStatus[0] <= 599 {
		metadata = map[string]string{"provider_status": strconv.Itoa(providerStatus[0])}
	}
	return datasourceStatus(codes.FailedPrecondition, reason, "source operation refused", metadata, 0)
}

func sourceInputRefused(err error) error {
	pointer := "/"
	var refusal *operations.Refusal
	if errors.As(err, &refusal) {
		pointer = refusal.Pointer
	}
	base := datasourceStatus(codes.InvalidArgument, "SOURCE_INPUT_REFUSED", "source input refused at "+pointer, nil, 0)
	detailed, detailErr := status.Convert(base).WithDetails(&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: pointer, Description: "input does not satisfy the declaration"}}})
	if detailErr != nil {
		return base
	}
	return detailed.Err()
}
func operationUnknown() error {
	return datasourceStatus(codes.FailedPrecondition, "SOURCE_OPERATION_OUTCOME_UNKNOWN", "source operation outcome is unknown; this effect will not be dispatched again", nil, 0)
}
func operationRateLimited(reset time.Time) error {
	retry := time.Until(reset)
	if retry <= 0 {
		retry = defaultRateLimitRetry
	}
	return datasourceStatus(codes.ResourceExhausted, DatasourceReasonRateLimited, "The provider rate limited the operation. Retry after the reset.", map[string]string{"reset_at": reset.UTC().Format(time.RFC3339)}, retry)
}

// InvokeSourceOperation runs only under the SDK's admitted effect. The callback
// records its wire response through receipts.Record in the supplied transaction.
// The attempt marker survives any uncertain provider outcome or commit failure;
// an external mutation cannot be rolled back with our Postgres transaction.
func (s *Service) InvokeSourceOperation(ctx context.Context, actor, org, source, operation string, input []byte, commit func(context.Context, *SourceOperationResult) error) (response *SourceOperationResult, returnedErr error) {
	effect, ok := receipts.EffectFromContext(ctx)
	if !ok || effect.Tenant != org || effect.Method != SourceOperationMethod || commit == nil {
		return nil, operationRefused("SOURCE_EFFECT_REQUIRED")
	}
	var declaration operations.Declaration
	defer func() {
		outcome := "refused"
		if response != nil && returnedErr == nil {
			outcome = "committed"
		}
		if returnedErr != nil {
			outcome = "refused"
			if status.Code(returnedErr) == codes.ResourceExhausted {
				outcome = "rate_limited"
			}
			for _, detail := range status.Convert(returnedErr).Details() {
				if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "SOURCE_OPERATION_OUTCOME_UNKNOWN" {
					outcome = "unresolved"
				}
			}
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.AuditSourceOperation(auditCtx, actor, org, source, declaration, effect.ID, outcome)
	}()
	src, admitted, err := s.SourceOperationAuthority(ctx, actor, org, source, operation, effect.ID, false)
	declaration = admitted
	if err != nil {
		return nil, err
	}
	prior, err := s.SourceOperationAttemptForActor(ctx, actor, org, effect.ID)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if !bytes.Equal(prior.RequestDigest, effect.RequestDigest) {
			return nil, operationRefused("SOURCE_EFFECT_REUSED")
		}
		if declaration.Effect != operations.ReadOnly {
			return nil, operationUnknown()
		}
		// A receipt-free read can be recovered after a crash or failed commit.
		// The SDK serialization hold and request digest still fence this effect.
		// Retain the marker: an uncertain local commit may have saved a receipt,
		// and the actor/request binding must survive recovery either way.
	}
	target, body, err := declaration.Route(src.API.BaseURL, input)
	if err != nil {
		return nil, sourceInputRefused(err)
	}
	if s.datasourceCipher == nil {
		return nil, operationUnavailable()
	}

	// The cipher and refresh lock are the same ones used by the sync path.
	cfg := apisource.Config{BaseURL: src.API.BaseURL, CredentialKind: src.API.CredentialKind, CredentialHeader: src.API.CredentialHeader, CredentialQueryParam: src.API.CredentialQueryParam, MaxOutputBytes: declaration.MaxOutputBytes}
	var credential string
	if src.API.CredentialKind == APICredentialKindOAuth2 {
		credential, err = s.resolveOAuth2AccessToken(ctx, src)
		cfg.CredentialKind = apisource.CredentialKindBearer
	} else {
		credential, err = s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(src.ID), src.CredentialSecretRef)
	}
	if err != nil {
		if errors.Is(err, ErrOAuth2ReauthRequired) {
			return nil, operationRefused("SOURCE_REAUTH_REQUIRED")
		}
		return nil, operationUnavailable()
	}
	scheduler := datasourceScheduler{s: s}
	budget := apiOperationBudget
	if err := scheduler.Acquire(ctx, connectorSource(src), budget, connector.PriorityInteractive); err != nil {
		var limited *connector.RateLimitedError
		if errors.As(err, &limited) {
			return nil, operationRateLimited(limited.ResetAt)
		}
		return nil, operationUnavailable()
	}
	attempts := s.store.(SourceOperationAttemptStore)
	var created bool
	err = s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		// Serialize with declaration replacement, then recheck the admitted digest.
		if _, err := s.store.LockDatasourceSourceCredentialRef(ctx, org, source); err != nil {
			return operationUnavailable()
		}
		all, err := s.store.(SourceOperationStore).ListSourceOperations(ctx, org, source)
		if err != nil {
			return operationUnavailable()
		}
		matched := false
		for _, d := range all {
			if d.Name == operation && d.Digest == declaration.Digest {
				matched = true
			}
		}
		if !matched {
			return operationRefused("SOURCE_DECLARATION_CHANGED")
		}
		prior, err := attempts.GetSourceOperationAttempt(ctx, org, effect.ID)
		if err != nil {
			return operationUnavailable()
		}
		if prior != nil {
			if !bytes.Equal(prior.RequestDigest, effect.RequestDigest) {
				return operationRefused("SOURCE_EFFECT_REUSED")
			}
			if declaration.Effect == operations.ReadOnly {
				return nil // Recover the same read under its existing marker.
			}
			return operationUnknown()
		}
		created, err = attempts.CreateSourceOperationAttempt(ctx, SourceOperationAttempt{OrgID: org, EffectID: effect.ID, ActorID: actor, SourceID: source, Operation: operation, DeclarationDigest: declaration.Digest, RequestDigest: effect.RequestDigest})
		if err != nil {
			return operationUnavailable()
		}
		if !created {
			return operationUnknown()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	client := APIOperationClient(apisource.New(cfg, credential))
	if s.newAPIOperationClient != nil {
		client = s.newAPIOperationClient(cfg, credential)
	}
	result, callErr := client.Do(ctx, declaration.Method, target, body)
	if callErr != nil {
		var failure *apisource.Failure
		errors.As(callErr, &failure)
		// A mutation with no trustworthy rejection remains unknown forever. This
		// includes a successful provider response whose body was lost or over cap.
		if failure != nil && failure.ProviderStatus == 429 {
			reset := time.Now().UTC().Add(failure.RetryAfter)
			if err := scheduler.Blocked(ctx, connectorSource(src), &connector.RateLimitedError{ResetAt: reset, Scope: "credential"}); err != nil {
				return nil, operationUnavailable()
			}
			if err := s.forgetSourceAttempt(ctx, org, effect.ID); err != nil {
				return nil, err
			}
			return nil, operationRateLimited(reset)
		}
		if failure != nil && failure.ProviderStatus >= 400 && failure.ProviderStatus < 500 {
			if err := s.forgetSourceAttempt(ctx, org, effect.ID); err != nil {
				return nil, err
			}
			return nil, operationRefused("SOURCE_PROVIDER_REFUSED", failure.ProviderStatus)
		}
		if declaration.Effect == operations.Mutation {
			return nil, operationUnknown()
		}
		if err := s.forgetSourceAttempt(ctx, org, effect.ID); err != nil {
			return nil, err
		}
		if failure != nil && failure.OutputRefused {
			return nil, operationRefused("SOURCE_OUTPUT_REFUSED", failure.ProviderStatus)
		}
		return nil, operationUnavailable()
	}
	if result == nil {
		if declaration.Effect == operations.ReadOnly {
			return nil, operationUnavailable()
		}
		return nil, operationUnknown()
	}
	if err := declaration.ValidateOutput(result.Body); err != nil {
		if declaration.Effect == operations.Mutation {
			return nil, operationUnknown()
		}
		if err := s.forgetSourceAttempt(ctx, org, effect.ID); err != nil {
			return nil, err
		}
		return nil, operationRefused("SOURCE_OUTPUT_REFUSED", result.StatusCode)
	}
	out := &SourceOperationResult{Output: result.Body, EffectID: effect.ID, CommittedAt: time.Now().UTC(), ProviderStatus: result.StatusCode}
	if err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error { return commit(ctx, out) }); err != nil {
		if declaration.Effect == operations.ReadOnly {
			return nil, operationUnavailable()
		}
		return nil, operationUnknown()
	}
	return out, nil
}

func (s *Service) forgetSourceAttempt(ctx context.Context, org, effect string) error {
	if err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		return s.store.(SourceOperationAttemptStore).DeleteSourceOperationAttempt(ctx, org, effect)
	}); err != nil {
		return operationUnavailable()
	}
	return nil
}

// SourceOperationAuditPayload accepts admitted identifiers and a closed outcome
// vocabulary. It never accepts an input, output, URL or error as audit content.
func SourceOperationAuditPayload(declaration operations.Declaration, effectID, outcome string) map[string]any {
	switch outcome {
	case "committed", "refused", "unresolved", "rate_limited":
	default:
		outcome = "refused"
	}
	name, effect := declaration.Name, declaration.Effect
	if name == "" {
		name = "unknown"
	}
	if effect != operations.ReadOnly && effect != operations.Mutation {
		effect = "UNKNOWN"
	}
	return map[string]any{"operation": name, "effect": effect, "effect_id": effectID, "outcome": outcome}
}

func (s *Service) AuditSourceOperation(ctx context.Context, actor, org, source string, declaration operations.Declaration, effectID, outcome string) {
	s.emit(ctx, actor, "user", EventDatasourceOperationInvoked, "datasource", source, org, SourceOperationAuditPayload(declaration, effectID, outcome))
}
