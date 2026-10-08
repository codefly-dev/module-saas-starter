package main

import (
	"context"

	accountsv1 "auth-gateway/external/saas-starter/accounts"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const checkWorkContextRevisionMethod = "/saas.accounts.v1.WorkContextService/CheckAuthorizationRevision"

// checkCurrentWorkContext calls the existing consumer revocation oracle as the
// gateway itself. claims must first pass signature, issuer and target-audience
// verification. No module credential is borrowed and no caller identity header
// contributes to this request. Accounts owns current RBAC/delegation decisions.
func (s *ExtAuthz) checkCurrentWorkContext(ctx context.Context, claims *basev0.WorkContextV1) error {
	if s == nil || s.backendConn == nil || s.internalToken == "" {
		return status.Error(codes.Unavailable, "work context authority unavailable")
	}
	if claims == nil || claims.GetTenantId() == "" || claims.GetOwnerPrincipalId() == "" || claims.GetAuthorizationRevision() == 0 {
		return status.Error(codes.PermissionDenied, "incomplete work context authority")
	}
	owner, err := revisionSubject(claims.GetOwnerPrincipalId(), claims.GetAuthorityScopes())
	if err != nil {
		return err
	}
	request := &accountsv1.CheckAuthorizationRevisionRequest{
		OrgId: claims.GetTenantId(), OwnerPrincipalId: claims.GetOwnerPrincipalId(),
		AuthorizationRevision: claims.GetAuthorizationRevision(),
		Subjects:              []*accountsv1.WorkContextRevisionSubject{owner},
	}
	for _, actor := range claims.GetActorChain() {
		if actor == nil {
			return status.Error(codes.PermissionDenied, "invalid work context actor")
		}
		subject, err := revisionSubject(actor.GetPrincipalId(), actor.GetGrantedScopes())
		if err != nil {
			return err
		}
		request.Subjects = append(request.Subjects, subject)
	}
	// No inherited caller credentials or identity metadata cross this internal hop.
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-codefly-internal-token", s.internalToken))
	return s.backendConn.Invoke(ctx, checkWorkContextRevisionMethod, request, &emptypb.Empty{})
}

func revisionSubject(principal string, scopes []*basev0.WorkScopeV1) (*accountsv1.WorkContextRevisionSubject, error) {
	if principal == "" || len(scopes) == 0 {
		return nil, status.Error(codes.PermissionDenied, "incomplete work context subject")
	}
	subject := &accountsv1.WorkContextRevisionSubject{PrincipalId: principal}
	for _, scope := range scopes {
		if scope == nil || scope.GetResourceKind() == "" || len(scope.GetActions()) == 0 {
			return nil, status.Error(codes.PermissionDenied, "invalid work context scope")
		}
		subject.Scopes = append(subject.Scopes, &accountsv1.WorkContextScope{
			ResourceKind: scope.GetResourceKind(), Actions: append([]string(nil), scope.GetActions()...),
			ResourceIds: append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return subject, nil
}
