package adapters

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
		// Aborted, not FailedPrecondition: the caller can make this write valid by
		// re-reading and naming the revision. Sharing a code with the tombstone
		// refusal — which must never be retried — left a client unable to tell the
		// recoverable case from the permanent one, so it could retry neither.
		return status.Error(codes.Aborted, "solution registration revision required")
	case errors.Is(err, business.ErrSolutionRegistrationTombstoned):
		return status.Error(codes.FailedPrecondition, "solution registration is tombstoned")
	case errors.Is(err, business.ErrSolutionPublisherMismatch):
		return status.Error(codes.PermissionDenied, "solution registration is owned by another publisher")
	case errors.Is(err, business.ErrSolutionRegistrationHalfMissing):
		return status.Error(codes.InvalidArgument, "solution registration must carry exactly one half")
	case errors.Is(err, business.ErrSolutionRegistrationIdentityRequired):
		return status.Error(codes.InvalidArgument, "solution registration requires a solution id and publisher")
	case errors.Is(err, business.ErrSolutionAuditDeclarationRejected):
		// InvalidArgument, not FailedPrecondition or PermissionDenied: the
		// registrant must change its declaration (or the operator its
		// binding), and no retry of this manifest — nor a re-read of the
		// revision — will ever succeed. FailedPrecondition is already the
		// tombstone refusal, which the gateway relays as a conflict to re-read.
		// The message names the event and the rule it broke; the ErrorInfo
		// reason is what a client keys on, since InvalidArgument alone also
		// covers a malformed write.
		return solutionAuditDeclarationRejected(err)
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
//
// It deliberately never sets RuntimeBoundary, and NO path here ever does
// (issue #1015). Every read of the registry goes through this one function —
// the whole-registry snapshot the gateway and the frontend cache, a
// deregistration, and a registrant's own write alike — and the seed behind that
// boundary is the only thing standing between one solution's runs and another's
// now that the derived boundary is stable for the life of the registration.
// A solution does not need it: accounts derives and seals the boundary from the
// credential the solution already presents, so nothing above this service
// reads, sends or stores one. The field stays on the message so that
// TestSolutionRegistrationResponsesCarryNoRuntimeBoundary can hold every
// response to that, and a change that starts populating it fails rather than
// ships.
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

// SolutionAuditDeclarationRejectedReason is the google.rpc.ErrorInfo reason a
// refused audit event declaration carries, under SolutionRegistryErrorDomain.
// The auth-gateway keys its 422 on it — it is the stable signal, where the
// message is prose for the registrant — so it is a wire contract: the gateway
// holds the same two strings, and a test on each side pins them.
const (
	SolutionAuditDeclarationRejectedReason = "SOLUTION_AUDIT_DECLARATION_REJECTED"
	SolutionRegistryErrorDomain            = "accounts.saas.codefly.dev"
)

func solutionAuditDeclarationRejected(err error) error {
	rejected := status.New(codes.InvalidArgument, err.Error())
	detailed, detailErr := rejected.WithDetails(&errdetails.ErrorInfo{
		Reason: SolutionAuditDeclarationRejectedReason,
		Domain: SolutionRegistryErrorDomain,
	})
	if detailErr != nil {
		return rejected.Err()
	}
	return detailed.Err()
}
