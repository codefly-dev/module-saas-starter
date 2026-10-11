package adapters

import (
	"context"
	"errors"

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
	// Writing the ceiling every module is derived inside is a platform-role
	// change in all but name, so it asks for the step-up a role grant does.
	if err := requireMFA(ctx, actorID); err != nil {
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
