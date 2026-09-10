package adapters

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SolutionRegistryServer serves the durable solution registry (issue #534) on
// the accounts internal listener. The auth-gateway is its only client: it
// writes its own backend half, brokers the frontend's half, and reads the
// snapshot both surfaces rebuild their caches from.
type SolutionRegistryServer struct {
	gen.UnsafeSolutionRegistryServiceServer
}

var solutionRegistrySingleton = &SolutionRegistryServer{}

// SolutionRegistrySingleton returns the shared server instance.
func SolutionRegistrySingleton() *SolutionRegistryServer { return solutionRegistrySingleton }

// solutionRegistryError maps the registry's refusals onto gRPC codes a client
// can act on: Aborted means "re-read and retry", FailedPrecondition means "your
// request cannot be made valid by retrying it as-is".
func solutionRegistryError(err error) error {
	switch {
	case errors.Is(err, business.ErrSolutionRegistrationNotFound):
		return status.Error(codes.NotFound, "solution registration not found")
	case errors.Is(err, business.ErrSolutionRegistrationStale):
		return status.Error(codes.Aborted, "solution registration revision is stale")
	case errors.Is(err, business.ErrSolutionRegistrationRevisionRequired):
		return status.Error(codes.FailedPrecondition, "solution registration revision required")
	case errors.Is(err, business.ErrSolutionRegistrationTombstoned):
		return status.Error(codes.FailedPrecondition, "solution registration is tombstoned")
	case errors.Is(err, business.ErrSolutionPublisherMismatch):
		return status.Error(codes.PermissionDenied, "solution registration is owned by another publisher")
	case errors.Is(err, business.ErrSolutionRegistrationHalfMissing):
		return status.Error(codes.InvalidArgument, "solution registration must carry exactly one half")
	case errors.Is(err, business.ErrSolutionRegistrationIdentityRequired):
		return status.Error(codes.InvalidArgument, "solution registration requires a solution id and publisher")
	default:
		return err
	}
}

func (s *SolutionRegistryServer) PutSolutionRegistration(
	ctx context.Context, req *gen.PutSolutionRegistrationRequest,
) (*gen.SolutionRegistration, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	write := business.SolutionRegistrationWrite{
		SolutionID:       req.GetSolutionId(),
		Publisher:        req.GetPublisher(),
		ExpectedRevision: req.ExpectedRevision,
		Lease:            time.Duration(req.GetLeaseSeconds()) * time.Second,
	}
	if half := req.GetFrontend(); half != nil {
		write.Frontend = &business.SolutionFrontendRegistration{
			Manifest:        half.GetManifest(),
			ContractVersion: half.GetContractVersion(),
		}
	}
	if half := req.GetBackend(); half != nil {
		write.Backend = &business.SolutionBackendRegistration{
			Upstream:        half.GetUpstream(),
			ServiceAlias:    half.GetServiceAlias(),
			ContractVersion: half.GetContractVersion(),
		}
	}
	record, err := service.PutSolutionRegistration(ctx, write)
	if err != nil {
		return nil, solutionRegistryError(err)
	}
	return solutionRegistrationProto(record), nil
}

func (s *SolutionRegistryServer) DeleteSolutionRegistration(
	ctx context.Context, req *gen.DeleteSolutionRegistrationRequest,
) (*gen.SolutionRegistration, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	record, err := service.DeleteSolutionRegistration(ctx, req.GetSolutionId(), req.ExpectedRevision)
	if err != nil {
		return nil, solutionRegistryError(err)
	}
	return solutionRegistrationProto(record), nil
}

func (s *SolutionRegistryServer) ListSolutionRegistrations(
	ctx context.Context, req *gen.ListSolutionRegistrationsRequest,
) (*gen.ListSolutionRegistrationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	records, revision, err := service.ListSolutionRegistrations(ctx, req.GetIncludeTombstoned())
	if err != nil {
		return nil, solutionRegistryError(err)
	}
	out := &gen.ListSolutionRegistrationsResponse{
		Registrations:    make([]*gen.SolutionRegistration, 0, len(records)),
		RegistryRevision: revision,
	}
	for _, record := range records {
		out.Registrations = append(out.Registrations, solutionRegistrationProto(record))
	}
	return out, nil
}

var solutionRegistrationStatusProto = map[business.SolutionRegistrationStatus]gen.SolutionRegistrationStatus{
	business.SolutionRegistrationActive:       gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE,
	business.SolutionRegistrationPending:      gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING,
	business.SolutionRegistrationExpired:      gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_EXPIRED,
	business.SolutionRegistrationIncompatible: gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE,
	business.SolutionRegistrationTombstoned:   gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED,
}

// solutionRegistrationProto resolves the derived status against the server
// clock as it serializes, so a lease that lapsed since the row was written is
// reported expired rather than active.
func solutionRegistrationProto(record *business.SolutionRegistration) *gen.SolutionRegistration {
	out := &gen.SolutionRegistration{
		SolutionId: record.SolutionID,
		Publisher:  record.Publisher,
		Revision:   record.Revision,
		Status:     solutionRegistrationStatusProto[record.Status(time.Now().UTC())],
		UpdatedAt:  timestamppb.New(record.UpdatedAt),
	}
	if half := record.Frontend; half != nil {
		out.Frontend = &gen.SolutionFrontendBinding{
			Revision:        half.Revision,
			Manifest:        half.Manifest,
			ContractVersion: half.ContractVersion,
			LeaseExpiresAt:  timestamppb.New(half.LeaseExpiresAt),
		}
	}
	if half := record.Backend; half != nil {
		out.Backend = &gen.SolutionBackendBinding{
			Revision:        half.Revision,
			Upstream:        half.Upstream,
			ServiceAlias:    half.ServiceAlias,
			ContractVersion: half.ContractVersion,
			LeaseExpiresAt:  timestamppb.New(half.LeaseExpiresAt),
		}
	}
	if record.TombstonedAt != nil {
		out.TombstonedAt = timestamppb.New(*record.TombstonedAt)
	}
	return out
}

// solutionRegistryConnectHandler serves the same server over Connect, so both
// protocols enforce the same authority and see the same identity.
type solutionRegistryConnectHandler struct {
	inner *SolutionRegistryServer
}

func (h *solutionRegistryConnectHandler) PutSolutionRegistration(ctx context.Context, req *connect.Request[gen.PutSolutionRegistrationRequest]) (*connect.Response[gen.SolutionRegistration], error) {
	return unary(ctx, req, h.inner.PutSolutionRegistration)
}

func (h *solutionRegistryConnectHandler) DeleteSolutionRegistration(ctx context.Context, req *connect.Request[gen.DeleteSolutionRegistrationRequest]) (*connect.Response[gen.SolutionRegistration], error) {
	return unary(ctx, req, h.inner.DeleteSolutionRegistration)
}

func (h *solutionRegistryConnectHandler) ListSolutionRegistrations(ctx context.Context, req *connect.Request[gen.ListSolutionRegistrationsRequest]) (*connect.Response[gen.ListSolutionRegistrationsResponse], error) {
	return unary(ctx, req, h.inner.ListSolutionRegistrations)
}
