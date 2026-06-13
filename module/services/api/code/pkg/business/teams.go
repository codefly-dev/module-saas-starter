package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	"api/pkg/gen"
)

// ListTeams returns all teams in an organization.
func (s *Service) ListTeams(ctx context.Context, req *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error) {
	w := wool.Get(ctx).In("ListTeams")

	var teams []*gen.Team
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		ts, err := s.store.ListTeams(ctx, req.OrgId)
		teams = ts
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list teams")
	}
	return &gen.ListTeamsResponse{Teams: teams}, nil
}

// AddTeamMember adds a member to a team. The team_id-only request
// shape forces a team→org resolve under WithBypass before entering
// the tenant-scoped tx that does the actual write.
func (s *Service) AddTeamMember(ctx context.Context, actorID string, req *gen.AddTeamMemberRequest) error {
	w := wool.Get(ctx).In("AddTeamMember")

	orgID, err := s.resolveTeamOrg(ctx, req.TeamId)
	if err != nil {
		return w.Wrapf(err, "cannot resolve team org")
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.AddTeamMember(ctx, req.TeamId, req.UserId, teamRoleToString(req.Role))
	}); err != nil {
		return w.Wrapf(err, "cannot add team member")
	}

	s.emit(ctx, actorID, "user", "team.member_added", "team", req.TeamId, orgID)
	return nil
}

// teamRoleToString maps the proto enum to the lowercase string the
// team_members.role CHECK constraint expects ('member','admin','owner').
// Unspecified defaults to 'member'.
func teamRoleToString(role gen.TeamRole) string {
	switch role {
	case gen.TeamRole_TEAM_ROLE_OWNER:
		return "owner"
	case gen.TeamRole_TEAM_ROLE_ADMIN:
		return "admin"
	default:
		return "member"
	}
}

// RemoveTeamMember removes a member from a team.
func (s *Service) RemoveTeamMember(ctx context.Context, actorID string, req *gen.RemoveTeamMemberRequest) error {
	w := wool.Get(ctx).In("RemoveTeamMember")

	orgID, err := s.resolveTeamOrg(ctx, req.TeamId)
	if err != nil {
		return w.Wrapf(err, "cannot resolve team org")
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.RemoveTeamMember(ctx, req.TeamId, req.UserId)
	}); err != nil {
		return w.Wrapf(err, "cannot remove team member")
	}

	s.emit(ctx, actorID, "user", "team.member_removed", "team", req.TeamId, orgID)
	return nil
}

// ListTeamMembers lists all members of a team.
func (s *Service) ListTeamMembers(ctx context.Context, req *gen.ListTeamMembersRequest) (*gen.ListTeamMembersResponse, error) {
	w := wool.Get(ctx).In("ListTeamMembers")

	orgID, err := s.resolveTeamOrg(ctx, req.TeamId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot resolve team org")
	}

	var members []*gen.TeamMembership
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		ms, err := s.store.ListTeamMembers(ctx, req.TeamId)
		members = ms
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list team members")
	}
	return &gen.ListTeamMembersResponse{Members: members}, nil
}

// teamOrgCacheKey memoizes a team→org resolution for the duration
// of a single request. Stamped by the handler layer's
// requireTeamAdmin (which has already paid for the bypass round-
// trip); read here to avoid a redundant second one.
type teamOrgCacheKey struct{ teamID string }

// WithCachedTeamOrgID stamps a team→org resolution onto ctx so the
// downstream Service.resolveTeamOrg call inside the same request
// skips a WithBypass round-trip. Called by adapters/rpcs.go right
// after requireTeamAdmin succeeds. Exported so adapters can use it.
func WithCachedTeamOrgID(ctx context.Context, teamID, orgID string) context.Context {
	if teamID == "" || orgID == "" {
		return ctx
	}
	return context.WithValue(ctx, teamOrgCacheKey{teamID: teamID}, orgID)
}

// resolveTeamOrg looks up the org_id for a team, bypassing RLS for
// the lookup itself. The caller then re-enters WithOrgTx with that
// org for the actual op — that's the tenant-scoped path. Returns
// an error wrapped at PermissionDenied semantics when the team
// doesn't exist (so a wrong team_id can't be used to probe other
// orgs).
//
// If ctx already carries a cached resolution from this same request
// (set by adapters' WithCachedTeamOrgID after requireTeamAdmin), we
// skip the WithBypass round-trip — saves 1 of 4 transactions per
// request on the team-mutation path.
func (s *Service) resolveTeamOrg(ctx context.Context, teamID string) (string, error) {
	if cached, ok := ctx.Value(teamOrgCacheKey{teamID: teamID}).(string); ok && cached != "" {
		return cached, nil
	}
	var orgID string
	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		o, err := s.store.GetTeamOrgID(ctx, teamID)
		orgID = o
		return err
	}); err != nil {
		return "", err
	}
	if orgID == "" {
		return "", wool.Get(ctx).NewError("team not found")
	}
	return orgID, nil
}
