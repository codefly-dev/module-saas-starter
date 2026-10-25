package business

import "context"

// ExposeHostInstallationAs is ExposeHostInstallation with the solution's role
// declaration given, as a delivery would supply it.
func (s *Service) ExposeHostInstallationAs(ctx context.Context, actor, org, id, team string, authority *SolutionAuthority) (*HostInstallationDetails, error) {
	details, err := s.ReadHostInstallation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return s.exposeHostInstallation(ctx, actor, org, details, team, authority)
}
