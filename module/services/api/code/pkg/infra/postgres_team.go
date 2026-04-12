package infra

import (
	"context"
	"time"

	"backend/pkg/gen"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PostgresStore) CreateTeam(ctx context.Context, team *gen.Team) error {
	w := wool.Get(ctx).In("CreateTeam")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		INSERT INTO teams (id, org_id, name, description, created_at)
		VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP)`,
		team.Id, team.OrgId, team.Name, team.Description,
	)
	if err != nil {
		return w.Wrapf(err, "failed to insert team")
	}
	return nil
}

func (s *PostgresStore) ListTeams(ctx context.Context, orgID string) ([]*gen.Team, error) {
	w := wool.Get(ctx).In("ListTeams")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT id, org_id, name, description, created_at
		FROM teams WHERE org_id = $1 ORDER BY name`, orgID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list teams")
	}
	defer rows.Close()

	var teams []*gen.Team
	for rows.Next() {
		var t gen.Team
		var createdAt time.Time
		var desc *string
		if err := rows.Scan(&t.Id, &t.OrgId, &t.Name, &desc, &createdAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan team")
		}
		if desc != nil {
			t.Description = *desc
		}
		t.CreatedAt = timestamppb.New(createdAt)
		teams = append(teams, &t)
	}
	return teams, nil
}

func (s *PostgresStore) AddTeamMember(ctx context.Context, teamID string, userID string, role string) error {
	w := wool.Get(ctx).In("AddTeamMember")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (team_id, user_id) DO UPDATE SET role = $3`,
		teamID, userID, role,
	)
	if err != nil {
		return w.Wrapf(err, "failed to add team member")
	}
	return nil
}

func (s *PostgresStore) RemoveTeamMember(ctx context.Context, teamID string, userID string) error {
	w := wool.Get(ctx).In("RemoveTeamMember")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`,
		teamID, userID,
	)
	if err != nil {
		return w.Wrapf(err, "failed to remove team member")
	}
	return nil
}

func (s *PostgresStore) ListTeamMembers(ctx context.Context, teamID string) ([]*gen.TeamMembership, error) {
	w := wool.Get(ctx).In("ListTeamMembers")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT team_id, user_id, role, joined_at
		FROM team_members WHERE team_id = $1
		ORDER BY joined_at`, teamID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list team members")
	}
	defer rows.Close()

	var members []*gen.TeamMembership
	for rows.Next() {
		var m gen.TeamMembership
		var role string
		var joinedAt time.Time
		if err := rows.Scan(&m.TeamId, &m.UserId, &role, &joinedAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan team member")
		}
		m.Role = parseTeamRole(role)
		m.JoinedAt = timestamppb.New(joinedAt)
		members = append(members, &m)
	}
	return members, nil
}

func parseTeamRole(role string) gen.TeamRole {
	switch role {
	case "owner":
		return gen.TeamRole_TEAM_ROLE_OWNER
	case "admin":
		return gen.TeamRole_TEAM_ROLE_ADMIN
	case "member":
		return gen.TeamRole_TEAM_ROLE_MEMBER
	default:
		return gen.TeamRole_TEAM_ROLE_UNSPECIFIED
	}
}
