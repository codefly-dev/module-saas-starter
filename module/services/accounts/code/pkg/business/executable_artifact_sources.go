package business

import (
	"context"
	"errors"
	"sort"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModuleExecutableArtifactSources observes the existing effective policy under
// the same current module/parent/member boundary as installation identity. It
// neither approves an artifact nor assigns exclusive namespace ownership.
func (s *Service) ModuleExecutableArtifactSources(ctx context.Context, caller ModuleCaller, tenant, owner, audience, installation, policyID string) (*gen.ModuleExecutableArtifactSourcesResponse, error) {
	ctx, releaseAuthority, err := s.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, artifactSourcesError(err)
	}
	defer releaseAuthority()
	policy, err := s.ExecutableArtifactPolicyFor(ctx, caller, tenant, audience, policyID)
	if err != nil {
		return nil, artifactSourcesError(err)
	}
	current, err := s.ModuleCurrentInstallation(ctx, caller, tenant, owner, audience, installation)
	if err != nil {
		return nil, artifactSourcesError(err)
	}
	sources := make([]string, 0)
	for source, binding := range policy.Sources {
		if binding == current.BindingId {
			sources = append(sources, source)
		}
	}
	sort.Strings(sources)
	return &gen.ModuleExecutableArtifactSourcesResponse{Installation: current, PolicyId: policyID, Sources: sources}, nil
}

func artifactSourcesError(err error) error {
	var stored *StoreError
	if errors.As(err, &stored) && (stored.StoreErrorType == ErrTypeNotFound || stored.StoreErrorType == ErrTypePermission) {
		return status.Error(codes.PermissionDenied, "artifact sources unavailable")
	}
	switch status.Code(err) {
	case codes.PermissionDenied, codes.NotFound:
		return status.Error(codes.PermissionDenied, "artifact sources unavailable")
	case codes.Unknown, codes.Internal:
		return status.Error(codes.Unavailable, "artifact source authority unavailable")
	default:
		return err
	}
}
