package adapters

import (
	"context"

	"accounts/pkg/business"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The named refusals of the Work Context read oracles (CheckAuthorizationRevision,
// AuthorizeEvidenceRead) when the asking module does not prove itself.
const (
	errOracleCallerUnidentified = "the calling module is not identified: present exactly one module Work Context, issued from the module's workload credential; a shared internal token is not accepted"
	errOracleCallerUnavailable  = "the calling module cannot be authenticated: the module authority is not configured on this host"
	errOracleCallerNotInstalled = "the calling module is not installed in organization %s, so it is answered nothing about it"
)

// requireCallingModule authenticates the module asking a Work Context read
// oracle, with the proof every method on the module capability surface asks:
// the module's own Work Context, which the authority issued from the module's
// workload credential, verified here and held to the module's current grant
// and installation. The answer is one bit about principals the request names,
// so the caller must be a module that exists and is installed now; a static
// token every workload holds names no module and is not accepted in its place.
//
// Unauthenticated when no single module Work Context is presented, when the
// one presented does not verify, or when it names a module this host does not
// admit (not registered, installed nowhere, tenant moved): the module drops its
// context and mints again. Unavailable when this host cannot authenticate
// modules at all.
//
// The answer is then confined to orgID: the calling module must be installed
// in the organisation it asks about, or it is answered nothing. An
// organisation it is not installed in and one that does not exist are refused
// alike, so the refusal tells it nothing about either.
func (s *WorkContextAuthorityServer) requireCallingModule(ctx context.Context, orgID string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(workContextHeader)
	if len(values) != 1 || values[0] == "" {
		return status.Error(codes.Unauthenticated, errOracleCallerUnidentified)
	}
	if service == nil || s == nil || !s.configured() {
		return status.Error(codes.Unavailable, errOracleCallerUnavailable)
	}
	caller, err := s.VerifyModuleWorkContext(ctx, values[0])
	if err != nil {
		return err
	}
	if _, err = service.AuthorizeModuleCapability(ctx, caller); err != nil {
		return err
	}
	if orgID == caller.BoundOrg {
		return nil
	}
	_, err = service.AuthorizeModuleCapability(ctx, business.ModuleCaller{PrincipalID: caller.PrincipalID, BoundOrg: orgID})
	if err != nil && status.Code(err) != codes.Unavailable {
		return status.Errorf(codes.PermissionDenied, errOracleCallerNotInstalled, orgID)
	}
	return err
}
