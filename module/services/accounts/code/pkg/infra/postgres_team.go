package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/gen"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PostgresStore) CreateTeam(ctx context.Context, team *gen.Team) error {
	w := wool.Get(ctx).In("CreateTeam")
	executor := s.getQueryExecutor(ctx)

	// parent_team_id is NULL for a root team (FK forbids '').
	var parent any
	if team.ParentTeamId != "" {
		parent = team.ParentTeamId
	}
	_, err := executor.Exec(ctx, `
		INSERT INTO teams (id, org_id, name, description, parent_team_id, slug, path, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_TIMESTAMP)`,
		team.Id, team.OrgId, team.Name, team.Description, parent, team.Slug, team.Path,
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
		SELECT id, org_id, name, description, parent_team_id, slug, path, created_at
		FROM teams WHERE org_id = $1 ORDER BY path`, orgID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list teams")
	}
	defer rows.Close()

	var teams []*gen.Team
	for rows.Next() {
		var t gen.Team
		var createdAt time.Time
		var desc, parent *string
		if err := rows.Scan(&t.Id, &t.OrgId, &t.Name, &desc, &parent, &t.Slug, &t.Path, &createdAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan team")
		}
		if desc != nil {
			t.Description = *desc
		}
		if parent != nil {
			t.ParentTeamId = *parent
		}
		t.CreatedAt = timestamppb.New(createdAt)
		teams = append(teams, &t)
	}
	return teams, nil
}

// GetTeamPath returns (orgID, path) for a team — the parent lookup CreateTeam
// uses to derive a child's path. ("", "") with no error when not found.
func (s *PostgresStore) GetTeamPath(ctx context.Context, teamID string) (string, string, error) {
	w := wool.Get(ctx).In("GetTeamPath")
	executor := s.getQueryExecutor(ctx)

	var orgID, path string
	err := executor.QueryRow(ctx,
		`SELECT org_id, path FROM teams WHERE id = $1`, teamID,
	).Scan(&orgID, &path)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", nil
		}
		return "", "", w.Wrapf(err, "failed to get team path")
	}
	return orgID, path, nil
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

// GetTeamOrgID looks up the org owning teamID. Returns "" with no
// error when the team isn't found (or RLS hides it from this
// caller — callers should wrap in WithBypass when they need the
// lookup to succeed regardless of current tenant context).
func (s *PostgresStore) GetTeamOrgID(ctx context.Context, teamID string) (string, error) {
	w := wool.Get(ctx).In("GetTeamOrgID")
	executor := s.getQueryExecutor(ctx)

	var orgID string
	err := executor.QueryRow(ctx,
		`SELECT org_id FROM teams WHERE id = $1`, teamID,
	).Scan(&orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", w.Wrapf(err, "failed to get team org")
	}
	return orgID, nil
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
