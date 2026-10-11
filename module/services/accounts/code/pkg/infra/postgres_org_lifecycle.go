package infra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UpdateOrganization renames an organization or changes its slug. An archived
// organization is not found: it is no longer anyone's to edit. A slug another
// organization holds (idx_organizations_slug is UNIQUE on LOWER(slug)) is a
// conflict, never a silent rename of the other.
func (s *PostgresStore) UpdateOrganization(ctx context.Context, id, name, slug string) (*gen.Organization, error) {
	w := wool.Get(ctx).In("UpdateOrganization")
	var org gen.Organization
	var createdAt time.Time
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		UPDATE organizations
		   SET name = $2, slug = $3, updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND archived_at IS NULL
		RETURNING id, name, slug, owner_id, created_at`,
		id, name, slug,
	).Scan(&org.Id, &org.Name, &org.Slug, &org.OwnerId, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(fmt.Errorf("organization %s not found", id), business.ErrTypeNotFound)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, business.NewStoreError(fmt.Errorf("slug %q is already taken", slug), business.ErrTypeConflict)
		}
		return nil, w.Wrapf(err, "failed to update organization")
	}
	org.CreatedAt = timestamppb.New(createdAt)
	return &org, nil
}

// ArchiveOrganization marks an organization archived and releases its slug, so
// a new organization may take the name again. The released slug keeps the
// original as a prefix and appends the id's random tail, which is unique; the
// original is recorded by the caller's audit event. An organization already
// archived is not found.
func (s *PostgresStore) ArchiveOrganization(ctx context.Context, orgID, actorID string) error {
	w := wool.Get(ctx).In("ArchiveOrganization")
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE organizations
		   SET archived_at = CURRENT_TIMESTAMP,
		       archived_by = $2,
		       slug = left(slug, 40) || '-deleted-' || right(id::text, 12),
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND archived_at IS NULL`,
		orgID, nilIfEmpty(actorID),
	)
	if err != nil {
		return w.Wrapf(err, "failed to archive organization")
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(fmt.Errorf("organization %s not found", orgID), business.ErrTypeNotFound)
	}
	return nil
}

// LockAllOrgMembers stabilizes the roster under the administration lock and
// takes every member's canonical advisory lock in sorted order, without a
// trigger-producing write. Keep the transaction open through removal; deletion
// must also lock all installations before any authorization or session trigger.
func (s *PostgresStore) LockAllOrgMembers(ctx context.Context, orgID string) ([]string, error) {
	org, err := business.ParseID(orgID)
	if err != nil {
		return nil, err
	}
	orgID = org.String()
	// The administration lock keeps the roster stable while all membership
	// locks are acquired. Take no trigger-producing write before these locks.
	if err := s.LockOrgAdministration(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT user_id FROM organization_members WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	slices.Sort(members)
	for _, userID := range members {
		if err := s.LockOrgMembership(ctx, orgID, userID); err != nil {
			return nil, err
		}
	}
	return members, nil
}

// RemoveLockedOrgMembers deletes only the roster returned by LockAllOrgMembers
// in this transaction, even if an uncoordinated insert occurs. Team memberships
// cascade, and the membership triggers revoke sessions and advance authority.
// During organization deletion the installation batch must be locked and
// uninstalled before this trigger-producing write.
func (s *PostgresStore) RemoveLockedOrgMembers(ctx context.Context, orgID string, members []string) (int64, error) {
	if storetx.Tx(ctx) == nil {
		return 0, errors.New("locked membership removal requires a tenant transaction")
	}
	org, err := business.ParseID(orgID)
	if err != nil {
		return 0, err
	}
	tag, err := s.getQueryExecutor(ctx).Exec(ctx,
		`DELETE FROM organization_members WHERE org_id = $1 AND user_id = ANY($2::uuid[])`, org.String(), members)
	if err != nil {
		return 0, wool.Get(ctx).In("RemoveLockedOrgMembers").Wrapf(err, "failed to remove organization members")
	}
	return tag.RowsAffected(), nil
}

// RemoveAllOrgMembers locks and deletes an organization's roster in the caller's
// transaction. Call before other authorization/session writes. A deletion that
// also uninstalls must use the separate lock and removal phases above.
func (s *PostgresStore) RemoveAllOrgMembers(ctx context.Context, orgID string) (int64, error) {
	members, err := s.LockAllOrgMembers(ctx, orgID)
	if err != nil {
		return 0, err
	}
	return s.RemoveLockedOrgMembers(ctx, orgID, members)
}

// RevokeOrgAPIKeys revokes every live API key of an organization.
func (s *PostgresStore) RevokeOrgAPIKeys(ctx context.Context, orgID string) (int64, error) {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx,
		`UPDATE api_keys SET revoked_at = NOW() WHERE organization_id = $1 AND revoked_at IS NULL`, orgID)
	if err != nil {
		return 0, wool.Get(ctx).In("RevokeOrgAPIKeys").Wrapf(err, "failed to revoke organization api keys")
	}
	return tag.RowsAffected(), nil
}

// RevokeOrgPendingInvitations revokes every pending invitation of an
// organization, as RevokeInvitation does one at a time.
func (s *PostgresStore) RevokeOrgPendingInvitations(ctx context.Context, orgID string) (int64, error) {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE invitations
		   SET status = 'revoked', revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
		 WHERE org_id = $1 AND status = 'pending'`, orgID)
	if err != nil {
		return 0, wool.Get(ctx).In("RevokeOrgPendingInvitations").Wrapf(err, "failed to revoke organization invitations")
	}
	return tag.RowsAffected(), nil
}

// ActiveInstallationIDs locks and lists an organization's unrevoked
// installations in ID order. The caller must keep this transaction open through
// the entire uninstall batch: every installation lock must precede the first
// grant deletion, whose trigger locks the organization's source-read revision.
func (s *PostgresStore) ActiveInstallationIDs(ctx context.Context, orgID string) ([]string, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT id FROM installations WHERE org_id = $1 AND status = 'active' ORDER BY id FOR UPDATE`, orgID)
	if err != nil {
		return nil, wool.Get(ctx).In("ActiveInstallationIDs").Wrapf(err, "failed to list installations")
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// CountOrgMembers counts an organization's memberships.
func (s *PostgresStore) CountOrgMembers(ctx context.Context, orgID string) (int, error) {
	var count int
	if err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT COUNT(*) FROM organization_members WHERE org_id = $1`, orgID,
	).Scan(&count); err != nil {
		return 0, wool.Get(ctx).In("CountOrgMembers").Wrapf(err, "failed to count organization members")
	}
	return count, nil
}

// ListAllOrganizations is the platform view: every organization, whether or not
// the caller belongs to it, with its member count. It must run under the
// control plane; tenant traffic sees one organization at most. Ordered by name
// then id, so offset pagination is stable.
func (s *PostgresStore) ListAllOrganizations(
	ctx context.Context,
	query string,
	includeArchived bool,
	limit, offset int,
) ([]*gen.PlatformOrganization, error) {
	w := wool.Get(ctx).In("ListAllOrganizations")
	pattern := "%" + escapeLike(strings.ToLower(strings.TrimSpace(query))) + "%"
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT o.id, o.name, o.slug, o.owner_id, o.created_at, o.archived_at,
		       (SELECT COUNT(*) FROM organization_members om WHERE om.org_id = o.id)
		  FROM organizations o
		 WHERE ($1 = '%%' OR LOWER(o.name) LIKE $1 OR LOWER(o.slug) LIKE $1)
		   AND ($2 OR o.archived_at IS NULL)
		 ORDER BY LOWER(o.name), o.id
		 LIMIT $3 OFFSET $4`,
		pattern, includeArchived, limit, offset,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list organizations")
	}
	defer rows.Close()
	var out []*gen.PlatformOrganization
	for rows.Next() {
		var org gen.Organization
		var createdAt time.Time
		var archivedAt *time.Time
		var members int32
		if err := rows.Scan(&org.Id, &org.Name, &org.Slug, &org.OwnerId, &createdAt, &archivedAt, &members); err != nil {
			return nil, w.Wrapf(err, "failed to scan organization")
		}
		org.CreatedAt = timestamppb.New(createdAt)
		if archivedAt != nil {
			org.ArchivedAt = timestamppb.New(*archivedAt)
		}
		out = append(out, &gen.PlatformOrganization{Organization: &org, MemberCount: members})
	}
	return out, rows.Err()
}

// escapeLike makes a user-typed search term literal inside a LIKE pattern.
func escapeLike(term string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
}
