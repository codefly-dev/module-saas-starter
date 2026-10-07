package adapters

import (
	"context"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SolutionRegistryServer serves the durable solution registry (issue #534) on
// the accounts internal listener. The auth-gateway is its only client: it
// reads the snapshot both product surfaces rebuild their caches from.
type SolutionRegistryServer struct {
	gen.UnsafeSolutionRegistryServiceServer
}

var solutionRegistrySingleton = &SolutionRegistryServer{}

// SolutionRegistrySingleton returns the shared server instance.
func SolutionRegistrySingleton() *SolutionRegistryServer { return solutionRegistrySingleton }

func (s *SolutionRegistryServer) ListSolutionRegistrations(
	ctx context.Context, req *gen.ListSolutionRegistrationsRequest,
) (*gen.ListSolutionRegistrationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	records, revision, err := service.ListSolutionRegistrations(ctx, req.GetIncludeTombstoned())
	if err != nil {
		return nil, err
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
	business.SolutionRegistrationIncompatible: gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE,
	business.SolutionRegistrationTombstoned:   gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED,
}

// solutionDeclaredKindProto maps the stored kind onto the wire. A kind the map
// does not hold projects as UNSPECIFIED, which both routing surfaces refuse by
// name: the zero value is not a kind, so a record carrying one is unroutable
// rather than routed as whichever kind needs the least authority.
var solutionDeclaredKindProto = map[business.SolutionDeclaredKind]gen.SolutionDeclaredKind{
	business.SolutionDeclaredKindSolution: gen.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION,
	business.SolutionDeclaredKindModule:   gen.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE,
}

// solutionRegistrationProto serializes the derived declaration status.
func solutionRegistrationProto(record *business.SolutionRegistration) *gen.SolutionRegistration {
	out := &gen.SolutionRegistration{
		SolutionId: record.SolutionID,
		Publisher:  record.Publisher,
		Revision:   record.Revision,
		Status:     solutionRegistrationStatusProto[record.Status()],
		UpdatedAt:  timestamppb.New(record.UpdatedAt),
	}
	if half := record.Frontend; half != nil {
		out.Frontend = &gen.SolutionFrontendBinding{
			Revision:        half.Revision,
			Manifest:        half.Manifest,
			ContractVersion: half.ContractVersion,
		}
	}
	if half := record.Backend; half != nil {
		out.Backend = &gen.SolutionBackendBinding{
			Revision:        half.Revision,
			Upstream:        half.Upstream,
			ServiceAlias:    half.ServiceAlias,
			ContractVersion: half.ContractVersion,
		}
	}
	if record.TombstonedAt != nil {
		out.TombstonedAt = timestamppb.New(*record.TombstonedAt)
	}
	if declared := record.Declared; declared != nil {
		out.Declared = &gen.SolutionDeclaredBinding{
			BindingId:  declared.BindingID,
			Generation: declared.Generation,
			Release:    declared.Release,
			TargetId:   declared.TargetID,
			Kind:       solutionDeclaredKindProto[declared.Kind],
		}
	}
	return out
}

// ListSolutionHostBindings serves the declared-presence read surface (issue
// #952): what delivery has shown this host, what the host applied, and why a
// desired generation is not the applied one.
//
// The observed half is attached from the registry record each binding applied
// into, so one answer distinguishes a solution that was never declared from one
// declared and not reporting — which is the distinction an operator cannot draw
// from the registry snapshot alone, because a binding whose first generation was
// refused has no registry record at all.
func (s *SolutionRegistryServer) ListSolutionHostBindings(
	ctx context.Context, req *gen.ListSolutionHostBindingsRequest,
) (*gen.ListSolutionHostBindingsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	records, err := service.ListSolutionHostBindings(ctx)
	if err != nil {
		return nil, err
	}
	registrations, _, err := service.ListSolutionRegistrations(ctx, true)
	if err != nil {
		return nil, err
	}
	bySolution := make(map[string]*business.SolutionRegistration, len(registrations))
	for _, registration := range registrations {
		bySolution[registration.SolutionID] = registration
	}
	out := &gen.ListSolutionHostBindingsResponse{
		Bindings: make([]*gen.SolutionHostBindingState, 0, len(records)),
	}
	for _, record := range records {
		out.Bindings = append(out.Bindings, solutionHostBindingStateProto(record, bySolution))
	}
	return out, nil
}

func solutionHostBindingStateProto(
	record *business.SolutionHostBindingRecord,
	bySolution map[string]*business.SolutionRegistration,
) *gen.SolutionHostBindingState {
	state := &gen.SolutionHostBindingState{
		BindingId:         record.BindingID,
		HostCoordinate:    record.HostCoordinate,
		HostComponent:     record.HostComponent,
		PendingGeneration: record.PendingGeneration(),
		PendingReason:     record.PendingReason,
		UpdatedAt:         timestamppb.New(record.UpdatedAt),
	}
	if record.PendingSince != nil {
		state.PendingSince = timestamppb.New(*record.PendingSince)
	}
	if desired := record.Desired; desired != nil {
		state.Desired = solutionHostBindingGenerationProto(*desired)
	}
	if applied := record.Applied; applied != nil {
		state.Applied = &gen.SolutionHostBindingAppliedGeneration{
			Generation: solutionHostBindingGenerationProto(applied.SolutionHostBindingGeneration),
			Removed:    applied.Removed,
			Routes:     applied.Routes,
			SolutionId: applied.SolutionID,
			Release:    applied.Release,
		}
		// The observed half. A binding whose applied generation is a tombstone
		// still names the record it withdrew, and that record's tombstone is the
		// answer "declared absent" rather than "never registered".
		if registration, found := bySolution[applied.SolutionID]; found {
			state.Registration = solutionRegistrationProto(registration)
		}
	}
	return state
}

func solutionHostBindingGenerationProto(
	generation business.SolutionHostBindingGeneration,
) *gen.SolutionHostBindingGeneration {
	return &gen.SolutionHostBindingGeneration{
		Generation: generation.Generation,
		Digest:     generation.Digest,
		Document:   generation.Document,
		At:         timestamppb.New(generation.At),
	}
}

// solutionRegistryConnectHandler serves the same server over Connect, so both
// protocols enforce the same authority and see the same identity.
type solutionRegistryConnectHandler struct {
	inner *SolutionRegistryServer
}

func (h *solutionRegistryConnectHandler) ListSolutionRegistrations(ctx context.Context, req *connect.Request[gen.ListSolutionRegistrationsRequest]) (*connect.Response[gen.ListSolutionRegistrationsResponse], error) {
	return unary(ctx, req, h.inner.ListSolutionRegistrations)
}

func (h *solutionRegistryConnectHandler) ListSolutionHostBindings(ctx context.Context, req *connect.Request[gen.ListSolutionHostBindingsRequest]) (*connect.Response[gen.ListSolutionHostBindingsResponse], error) {
	return unary(ctx, req, h.inner.ListSolutionHostBindings)
}

// SolutionRegistryErrorDomain is also the source-delegation ErrorInfo domain.
const SolutionRegistryErrorDomain = "accounts.saas.codefly.dev"
