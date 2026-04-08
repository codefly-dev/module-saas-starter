package infra

import (
	"context"
	"errors"

	"backend/pkg/gen"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

func (s *PostgresStore) CreateOrganization(ctx context.Context, org *gen.Organization) error {
	w := wool.Get(ctx).In("CreateOrganization")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		org.Id, org.Name, org.Slug, org.OwnerId,
	)
	if err != nil {
		return w.Wrapf(err, "failed to insert organization")
	}

	// Add owner as org member with 'owner' role
	_, err = executor.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, 'owner')`,
		org.Id, org.OwnerId,
	)
	if err != nil {
		return w.Wrapf(err, "failed to add owner as org member")
	}

	return nil
}

func (s *PostgresStore) GetOrganization(ctx context.Context, id string) (*gen.Organization, error) {
	w := wool.Get(ctx).In("GetOrganization")
	executor := s.getQueryExecutor(ctx)

	var org gen.Organization
	var createdAt time.Time

	err := executor.QueryRow(ctx, `
		SELECT id, name, slug, owner_id, created_at
		FROM organizations WHERE id = $1`, id,
	).Scan(&org.Id, &org.Name, &org.Slug, &org.OwnerId, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, w.Wrapf(err, "failed to get organization")
	}

	org.CreatedAt = timestamppb.New(createdAt)
	return &org, nil
}

func (s *PostgresStore) ListOrganizationsForUser(ctx context.Context, userID string) ([]*gen.Organization, error) {
	w := wool.Get(ctx).In("ListOrganizationsForUser")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT o.id, o.name, o.slug, o.owner_id, o.created_at
		FROM organizations o
		JOIN organization_members om ON o.id = om.org_id
		WHERE om.user_id = $1
		ORDER BY o.name`, userID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list organizations")
	}
	defer rows.Close()

	var orgs []*gen.Organization
	for rows.Next() {
		var org gen.Organization
		var createdAt time.Time
		if err := rows.Scan(&org.Id, &org.Name, &org.Slug, &org.OwnerId, &createdAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan organization")
		}
		org.CreatedAt = timestamppb.New(createdAt)
		orgs = append(orgs, &org)
	}
	return orgs, nil
}

func (s *PostgresStore) AddOrgMember(ctx context.Context, orgID string, userID string, role string) error {
	w := wool.Get(ctx).In("AddOrgMember")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = $3`,
		orgID, userID, role,
	)
	if err != nil {
		return w.Wrapf(err, "failed to add org member")
	}
	return nil
}

func (s *PostgresStore) RemoveOrgMember(ctx context.Context, orgID string, userID string) error {
	w := wool.Get(ctx).In("RemoveOrgMember")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2`,
		orgID, userID,
	)
	if err != nil {
		return w.Wrapf(err, "failed to remove org member")
	}
	return nil
}

func (s *PostgresStore) ListOrgMembers(ctx context.Context, orgID string) ([]*gen.OrgMembership, error) {
	w := wool.Get(ctx).In("ListOrgMembers")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT org_id, user_id, role, joined_at
		FROM organization_members WHERE org_id = $1
		ORDER BY joined_at`, orgID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list org members")
	}
	defer rows.Close()

	var members []*gen.OrgMembership
	for rows.Next() {
		var m gen.OrgMembership
		var role string
		var joinedAt time.Time
		if err := rows.Scan(&m.OrgId, &m.UserId, &role, &joinedAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan org member")
		}
		m.Role = parseOrgRole(role)
		m.JoinedAt = timestamppb.New(joinedAt)
		members = append(members, &m)
	}
	return members, nil
}

func parseOrgRole(role string) gen.OrgRole {
	switch role {
	case "owner":
		return gen.OrgRole_ORG_ROLE_OWNER
	case "admin":
		return gen.OrgRole_ORG_ROLE_ADMIN
	case "member":
		return gen.OrgRole_ORG_ROLE_MEMBER
	default:
		return gen.OrgRole_ORG_ROLE_UNSPECIFIED
	}
}
