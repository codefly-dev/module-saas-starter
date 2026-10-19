package adapters

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The scopes a machine credential must carry to touch the module envelope.
// They name this one ceiling and nothing else, so a key minted to write it can
// do only that.
const (
	moduleEnvelopeReadScope  = "platform.module_envelope:read"
	moduleEnvelopeWriteScope = "platform.module_envelope:write"
)

// requireModuleEnvelopeWrite decides what evidence this write needs, by the
// kind of credential presenting it.
//
// A SESSION writes the ceiling only with fresh AAL2 evidence: it replaces
// host-wide authority, and an administrator with no enrolled factor is refused
// rather than waved through.
//
// An API KEY presents no second factor and never will — there is no human at
// it to step up. Refusing it outright is what left this ceiling writable by
// nobody but a browser, with no operator surface at all: the design says an
// administrator writes it at runtime, and then offered no way to. So a key is
// admitted when, and only when, it carries the one scope that names this
// write. That scope IS the second factor's replacement, and it is a narrower
// one: a key is minted for this and nothing else, it is revocable the moment
// it is suspect, and it cannot be minted by another key
// (requireInteractiveSession on CreateAPIKey), so a machine credential can
// never widen itself into one. The write is recorded either way.
func requireModuleEnvelopeWrite(ctx context.Context) error {
	if credentialKindFromContext(ctx) == credentialKindAPIKey {
		return requireScope(ctx, moduleEnvelopeWriteScope)
	}
	return requireRecentMFA(ctx)
}

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
	// A session reads it as before; a key reads it only when it says so.
	if err := requireScope(ctx, moduleEnvelopeReadScope); err != nil {
		return nil, err
	}
	record, err := service.GetModuleEnvelope(ctx, actorID)
	if err != nil {
		return nil, moduleEnvelopeError(err, "cannot read the module envelope")
	}
	requests, err := service.ModuleAuthorityRequests(ctx, actorID)
	if err != nil {
		return nil, moduleEnvelopeError(err, "cannot read what the applied module manifests ask for")
	}
	response := &gen.GetModuleEnvelopeResponse{}
	var ceiling *business.ModuleEnvelope
	if record != nil {
		response.Envelope = moduleEnvelopeProto(record)
		// A stored revision this binary no longer validates allows nothing:
		// the reconcile pass refuses it the same way and names why.
		ceiling, _ = business.ParseModuleEnvelope(record.Document)
	}
	for _, request := range requests {
		authority, err := json.Marshal(request.Authority)
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot encode what an applied module manifest asks for")
		}
		allowed := false
		if ceiling != nil {
			_, allowed = ceiling.Principals[request.Principal]
		}
		response.Requested = append(response.Requested, &gen.ModuleAuthorityRequest{
			Principal:  request.Principal,
			Binding:    request.Binding,
			Generation: int64(request.Generation), //nolint:gosec // a generation is a small positive counter
			Alias:      request.Alias,
			Allowed:    allowed,
			Authority:  string(authority),
		})
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
	if err := requireModuleEnvelopeWrite(ctx); err != nil {
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
