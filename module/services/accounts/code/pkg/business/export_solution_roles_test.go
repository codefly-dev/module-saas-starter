package business

import (
	"context"
	"fmt"
)

// SolutionRoleIDForTest is the id of the derived role this host writes for the
// solution's role named role, at the target of installationID: what a host
// names to expose that installation to a team with the role. Compiled into
// tests only.
func (s *Service) SolutionRoleIDForTest(ctx context.Context, orgID, installationID, role string) (string, error) {
	row, err := s.organizationInstallation(ctx, orgID, installationID)
	if err != nil {
		return "", err
	}
	authority, why, err := s.deliveredTargetAuthority(ctx, row.Target)
	if err != nil {
		return "", err
	}
	if authority == nil {
		return "", fmt.Errorf("no solution roles: %s", why)
	}
	return s.solutionRoleID(ctx, orgID, row.Target.TargetID, row.Target.RouteAlias, authority, role)
}
