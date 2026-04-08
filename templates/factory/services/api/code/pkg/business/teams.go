package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	"backend/pkg/gen"
)

// ListTeams returns all teams in an organization.
func (s *Service) ListTeams(ctx context.Context, req *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error) {
	w := wool.Get(ctx).In("ListTeams")

	teams, err := s.store.ListTeams(ctx, req.OrgId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list teams")
	}
	return &gen.ListTeamsResponse{Teams: teams}, nil
}

// AddTeamMember adds a member to a team.
func (s *Service) AddTeamMember(ctx context.Context, actorID string, req *gen.AddTeamMemberRequest) error {
	w := wool.Get(ctx).In("AddTeamMember")

	if err := s.store.AddTeamMember(ctx, req.TeamId, req.UserId, req.Role.String()); err != nil {
		return w.Wrapf(err, "cannot add team member")
	}

	s.emit(ctx, actorID, "user", "team.member_added", "team", req.TeamId, "")
	return nil
}

// RemoveTeamMember removes a member from a team.
func (s *Service) RemoveTeamMember(ctx context.Context, actorID string, req *gen.RemoveTeamMemberRequest) error {
	w := wool.Get(ctx).In("RemoveTeamMember")

	if err := s.store.RemoveTeamMember(ctx, req.TeamId, req.UserId); err != nil {
		return w.Wrapf(err, "cannot remove team member")
	}

	s.emit(ctx, actorID, "user", "team.member_removed", "team", req.TeamId, "")
	return nil
}

// ListTeamMembers lists all members of a team.
func (s *Service) ListTeamMembers(ctx context.Context, req *gen.ListTeamMembersRequest) (*gen.ListTeamMembersResponse, error) {
	w := wool.Get(ctx).In("ListTeamMembers")

	members, err := s.store.ListTeamMembers(ctx, req.TeamId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list team members")
	}
	return &gen.ListTeamMembersResponse{Members: members}, nil
}
