package adapters

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// GetModuleEnvelope reads the current module envelope for a platform
// super_admin.
func (s *PlatformAdminServer) GetModuleEnvelope(ctx context.Context, req *gen.GetModuleEnvelopeRequest) (*gen.GetModuleEnvelopeResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, err
	}
	record, err := service.GetModuleEnvelope(ctx, actorID)
	if err != nil {
		return nil, moduleEnvelopeError(err, "cannot read the module envelope")
	}
	response := &gen.GetModuleEnvelopeResponse{}
	if record != nil {
		response.Envelope = moduleEnvelopeProto(record)
	}
	return response, nil
}

// PutModuleEnvelope writes the next revision of the module envelope for a
// platform super_admin. The business method refuses an impersonated session,
// validates the whole ceiling, and commits the row with its audit event.
func (s *PlatformAdminServer) PutModuleEnvelope(ctx context.Context, req *gen.PutModuleEnvelopeRequest) (*gen.PutModuleEnvelopeResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, err
	}
	// Writing the ceiling every module is derived inside replaces host-wide
	// authority, so it demands fresh AAL2 evidence — and an administrator with
	// no enrolled factor is refused rather than waved through, which is what
	// the if-enrolled gate would do for a password-only session.
	if err := requireRecentMFA(ctx); err != nil {
		return nil, err
	}
	record, err := service.PutModuleEnvelope(ctx, actorID, uint64(req.GetExpectedRevision()), []byte(req.GetDocument())) //nolint:gosec // protovalidate holds expected_revision >= 0
	if err != nil {
		return nil, moduleEnvelopeError(err, "cannot write the module envelope")
	}
	return &gen.PutModuleEnvelopeResponse{Envelope: moduleEnvelopeProto(record)}, nil
}

// moduleEnvelopeError maps the envelope's refusals to their codes. A
// validation refusal carries its reason, because the caller is a platform
// administrator fixing a document they wrote; a store fault does not.
func moduleEnvelopeError(err error, internal string) error {
	var storeErr *business.StoreError
	if errors.As(err, &storeErr) {
		switch storeErr.StoreErrorType {
		case business.ErrTypePermission:
			return status.Error(codes.PermissionDenied, "requires platform super_admin role")
		case business.ErrTypeValidation:
			return status.Error(codes.InvalidArgument, storeErr.Error())
		case business.ErrTypeConflict:
			return status.Error(codes.Aborted, "the module envelope was written since the expected revision; read it again")
		}
	}
	if errors.Is(err, business.ErrModuleEnvelopeUnavailable) {
		return status.Error(codes.FailedPrecondition, "this host has no module envelope store")
	}
	return status.Error(codes.Internal, internal)
}

func moduleEnvelopeProto(record *business.ModuleEnvelopeRecord) *gen.ModuleEnvelope {
	return &gen.ModuleEnvelope{
		Revision:  int64(record.Revision), //nolint:gosec // a revision is a small positive counter
		Document:  string(record.Document),
		WrittenBy: record.WrittenBy,
		WrittenAt: timestamppb.New(record.WrittenAt),
	}
}

func (h *platformAdminConnectHandler) GetModuleEnvelope(ctx context.Context, req *connect.Request[gen.GetModuleEnvelopeRequest]) (*connect.Response[gen.GetModuleEnvelopeResponse], error) {
	return unary(ctx, req, h.inner.GetModuleEnvelope)
}

func (h *platformAdminConnectHandler) PutModuleEnvelope(ctx context.Context, req *connect.Request[gen.PutModuleEnvelopeRequest]) (*connect.Response[gen.PutModuleEnvelopeResponse], error) {
	return unary(ctx, req, h.inner.PutModuleEnvelope)
}

// ResolveModuleTenantIdentity answers what a per-tenant module operation
// context would assert for one module, one of its bindings and one
// organization — or why it would be refused — for a platform super_admin.
//
// IT ISSUES NO CAPABILITY, and that is the shape rather than a limitation
// (issue #1121). The mint it describes requires the module pod's projected
// ServiceAccount token, held against the build its authority document approves;
// an operator's browser has none, and a path that answered one with a module's
// capability would be a second spelling of that module's identity. So this
// reads the decision and stops before the signature.
func (s *PlatformAdminServer) ResolveModuleTenantIdentity(
	ctx context.Context, req *gen.ResolveModuleTenantIdentityRequest,
) (*gen.ResolveModuleTenantIdentityResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, err
	}
	resolution, err := service.ResolveModuleTenantIdentity(ctx, req.GetPrefix(), req.GetBinding(), req.GetOrganizationId())
	if err != nil {
		return nil, translateGRPCError(err)
	}
	response := &gen.ResolveModuleTenantIdentityResponse{
		Refusal: resolution.Refusal,
		Recent:  make([]*gen.ModuleTenantIdentityMint, 0, len(resolution.Recent)),
	}
	if identity := resolution.Identity; identity != nil {
		response.Identity = &gen.ModuleTenantIdentity{
			PrincipalId:     identity.PrincipalID,
			Tenant:          identity.Tenant,
			Audience:        identity.Audience,
			Scopes:          identity.Scopes,
			LifetimeSeconds: int64(identity.Lifetime / time.Second),
			Revision:        identity.Revision,
			Shared:          identity.Shared,
		}
	}
	for _, mint := range resolution.Recent {
		response.Recent = append(response.Recent, &gen.ModuleTenantIdentityMint{
			Record:   mint.Record,
			Tenant:   mint.Tenant,
			Binding:  mint.Binding,
			Audience: mint.Audience,
			Scopes:   mint.Scopes,
			MintedAt: timestamppb.New(mint.MintedAt),
		})
	}
	return response, nil
}

func (h *platformAdminConnectHandler) ResolveModuleTenantIdentity(
	ctx context.Context, req *connect.Request[gen.ResolveModuleTenantIdentityRequest],
) (*connect.Response[gen.ResolveModuleTenantIdentityResponse], error) {
	return unary(ctx, req, h.inner.ResolveModuleTenantIdentity)
}
