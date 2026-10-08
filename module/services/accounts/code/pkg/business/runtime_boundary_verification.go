package business

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// VerifyModuleRuntimeBoundary only reads host-owned registration state. Its
// arguments must come from a verified capability, never caller-supplied IDs.
// Both registration halves must be live and compatible, matching serving status;
// backend-only task minting does not yet make a registration attestable.
// Scope authorization remains the consuming module's responsibility.
func (s *Service) VerifyModuleRuntimeBoundary(ctx context.Context, caller ModuleCaller, tenant, audience, boundary string) error {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return err
	}
	denied := status.Error(codes.PermissionDenied, "active runtime boundary required")
	if tenant == "" || boundary == "" || audience == "" || audience != grant.Prefix {
		return denied
	}
	if grant.Tenant != "" && grant.Tenant != caller.BoundOrg {
		return denied
	}
	if err := authorizeTenant(caller, grant, tenant); err != nil {
		return err
	}
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		registrations, _, err := s.store.ListSolutionRegistrations(ctx, false)
		if err != nil {
			return status.Error(codes.Unavailable, "runtime boundary registry unavailable")
		}
		now := time.Now().UTC()
		for _, registration := range registrations {
			if registration == nil || registration.Status(now) != SolutionRegistrationActive {
				continue
			}
			derived, err := SolutionRuntimeBoundary(registration.RuntimeBoundary, tenant)
			if err == nil && derived == boundary {
				return nil
			}
		}
		return denied
	})
}
