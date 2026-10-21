package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"context"
	"errors"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// VerifyIdempotent is a read-only consumer port over Core's verifier. Unsupported
// replay forms are rejected before invoking it, so this perimeter-authenticated
// operation cannot spend another consumer's capability.
func (s *WorkContextAuthorityServer) VerifyIdempotent(ctx context.Context, req *gen.VerifyIdempotentWorkContextRequest) (*gen.VerifyIdempotentWorkContextResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if !s.configured() {
		return nil, status.Error(codes.Unavailable, "Work Context authority is unavailable")
	}
	if req.GetExpectedIssuer() != s.issuer {
		return nil, status.Error(codes.InvalidArgument, "Invalid Work Context issuer")
	}
	decoded, err := workcontext.Decode(req.GetWorkContextToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid Work Context")
	}
	// These unverified fields may only refuse. No identity or permission is
	// derived until Core has verified the original, unmodified credential.
	shape := decoded.Context()
	if shape.GetReplayPolicy() != workcontext.ReplayIdempotent || shape.GetGrantHop() != nil {
		return nil, status.Error(codes.PermissionDenied, "Only idempotent Work Contexts are supported")
	}
	verified, err := s.verifyFor(ctx, req.GetWorkContextToken(), req.GetExpectedAudience())
	if err != nil {
		switch {
		case errors.Is(err, workcontext.ErrRevoked), errors.Is(err, workcontext.ErrNoSeal), errors.Is(err, workcontext.ErrNoBinding):
			return nil, status.Error(codes.FailedPrecondition, "Work Context authority changed")
		case errors.Is(err, workcontext.ErrInvalid), errors.Is(err, workcontext.ErrNotACoreToken):
			return nil, status.Error(codes.InvalidArgument, "Invalid Work Context")
		default:
			return nil, status.Error(codes.Unavailable, "Work Context authority is unavailable")
		}
	}
	claims := verified.Context()
	if err := s.checkVerifiedSubjects(ctx, claims); err != nil {
		return nil, err
	}
	actorID := claims.GetOwnerPrincipalId()
	if actor := verified.Actor(); actor != nil {
		actorID = actor.GetPrincipalId()
	}
	return &gen.VerifyIdempotentWorkContextResponse{
		TenantId: claims.GetTenantId(), OwnerPrincipalId: claims.GetOwnerPrincipalId(),
		CurrentActorPrincipalId: actorID, TaskId: claims.GetTaskId(),
		ExpiresAt:       timestamppb.New(time.Unix(claims.GetExpiresAtUnix(), 0)),
		EffectiveScopes: verificationScopes(verified.EffectiveScopes()),
	}, nil
}

func verificationScopes(scopes []*basev0.WorkScopeV1) []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, &gen.WorkContextScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIds:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

// checkVerifiedSubjects is called only after canonical authentication, including
// from the verifier replay hook before a single-use admission is committed.
func (s *WorkContextAuthorityServer) checkVerifiedSubjects(ctx context.Context, claims *basev0.WorkContextV1) error {
	if business.IsInstalledUseBinding(claims.GetOperationBinding().GetBindingId()) {
		ctx = auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId())
		return s.requireInstalledUseAuthority(ctx, claims.GetTenantId(), claims)
	}
	// Core checks the sealed revision and installation state. Keep the host's
	// current subject permission resolution as well: an unchanged counter is
	// not a substitute for the existing membership/permission check.
	revision := &gen.CheckAuthorizationRevisionRequest{
		OrgId: claims.GetTenantId(), OwnerPrincipalId: claims.GetOwnerPrincipalId(),
		AuthorizationRevision: claims.GetAuthorizationRevision(),
		Subjects: []*gen.WorkContextRevisionSubject{{
			PrincipalId: claims.GetOwnerPrincipalId(), Scopes: verificationScopes(claims.GetAuthorityScopes()),
		}},
	}
	for _, actor := range claims.GetActorChain() {
		revision.Subjects = append(revision.Subjects, &gen.WorkContextRevisionSubject{
			PrincipalId: actor.GetPrincipalId(), Scopes: verificationScopes(actor.GetGrantedScopes()),
		})
	}
	if binding := claims.GetOperationBinding().GetBindingId(); binding != "" {
		revision.OperationBindingId = &binding
	} else {
		none := true
		revision.SealsNoBinding = &none
	}
	if _, err := s.CheckAuthorizationRevision(ctx, revision); err != nil {
		if status.Code(err) == codes.Internal {
			return status.Error(codes.Unavailable, "Work Context authority is unavailable")
		}
		return err
	}
	return nil
}
