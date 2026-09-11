package infra

import (
	"context"
	"errors"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// OrganizationIDExists reports whether any organizations row already holds this
// id. Callers pass a caller-chosen id — the fixture seeder's declared ids, and
// the tenant a module principal grant declares — so the id is parsed here
// rather than handed straight to Postgres: organizations.id is a UUID column,
// and a malformed value would come back as "invalid input syntax for type
// uuid" from the driver, which tells the operator nothing about which id or
// why. This is the first check that runs on a declared id, so it is the error
// message the operator actually sees.
func (s *PostgresStore) OrganizationIDExists(ctx context.Context, id string) (bool, error) {
	w := wool.Get(ctx).In("OrganizationIDExists")
	if _, err := uuid.Parse(id); err != nil {
		return false, w.Wrapf(err, "organization id %q is not a uuid", id)
	}
	executor := s.getQueryExecutor(ctx)

	var exists bool
	if err := executor.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1)`, id,
	).Scan(&exists); err != nil {
		return false, w.Wrapf(err, "failed to check organization id")
	}
	return exists, nil
}

// GetOrganizationBySlug resolves the organization holding a slug, or (nil, nil)
// when the slug is free. idx_organizations_slug is UNIQUE on LOWER(slug), so
// the match is exact and at most one row can answer.
func (s *PostgresStore) GetOrganizationBySlug(ctx context.Context, slug string) (*gen.Organization, error) {
	w := wool.Get(ctx).In("GetOrganizationBySlug")
	executor := s.getQueryExecutor(ctx)

	var org gen.Organization
	var createdAt time.Time
	err := executor.QueryRow(ctx, `
		SELECT id, name, slug, owner_id, created_at
		FROM organizations WHERE LOWER(slug) = LOWER($1)`, slug,
	).Scan(&org.Id, &org.Name, &org.Slug, &org.OwnerId, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, w.Wrapf(err, "failed to get organization by slug")
	}
	org.CreatedAt = timestamppb.New(createdAt)
	return &org, nil
}

func (s *PostgresStore) CreateOrganization(ctx context.Context, org *gen.Organization) error {
	w := wool.Get(ctx).In("CreateOrganization")

	// Two inserts share atomicity (an ownerless org can't be managed,
	// so the org row WITHOUT the membership row is a leak). Reuse the
	// caller's tx if one is on context — that's where WithOrgTx /
	// WithControlPlane put it, and stacking a fresh BeginTxFunc would lose
	// the SET LOCAL ROLE / app.current_org_id state. If no tx, open
	// our own.
	exec := func(ctx context.Context) error {
		executor := s.getQueryExecutor(ctx)
		if _, err := executor.Exec(ctx, `
			INSERT INTO organizations (id, name, slug, owner_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			org.Id, org.Name, org.Slug, org.OwnerId,
		); err != nil {
			return w.Wrapf(err, "failed to insert organization")
		}
		if _, err := executor.Exec(ctx, `
			INSERT INTO organization_members (org_id, user_id, role)
			VALUES ($1, $2, 'owner')`,
			org.Id, org.OwnerId,
		); err != nil {
			return w.Wrapf(err, "failed to add owner as org member")
		}
		return nil
	}

	// If a tx is already on context (caller wrapped us in WithOrgTx
	// or WithControlPlane), reuse it.
	if _, hasTx := ctx.Value("tx").(pgx.Tx); hasTx {
		return exec(ctx)
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	}, func(tx pgx.Tx) error {
		ctx = context.WithValue(ctx, "tx", tx) //nolint:staticcheck // matches existing postgres.go pattern
		return exec(ctx)
	})
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

func (s *PostgresStore) OrgMemberExists(ctx context.Context, orgID string, userID string) (bool, error) {
	var exists bool
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM organization_members
			WHERE org_id = $1 AND user_id = $2
		)`, orgID, userID,
	).Scan(&exists)
	return exists, err
}

// CountOrgAdministrators counts the organization's eligible administrative
// memberships, and those of them held by somebody other than excludeUserID.
//
// Eligibility includes whether the identity can still authenticate, which means
// reading users. Request transactions cannot read a co-member's users row
// (migration 69), so they resolve it through the SECURITY DEFINER counter
// scoped to their own organization. Control-plane transactions set no tenant
// org and hold BYPASSRLS, so they evaluate the same predicate directly.
//
// The branch is on the tenant scope rather than on a fallback, deliberately: a
// direct join under app_tenant returns zero rows, and zero administrators reads
// as "this organization never had one", which the invariant exempts. Getting
// this wrong disables the rule instead of failing loudly.
func (s *PostgresStore) CountOrgAdministrators(ctx context.Context, orgID string, excludeUserID string) (int, int, error) {
	w := wool.Get(ctx).In("CountOrgAdministrators")
	executor := s.getQueryExecutor(ctx)

	var scopedOrg string
	if err := executor.QueryRow(ctx,
		`SELECT coalesce(pg_catalog.current_setting('app.current_org_id', true), '')`,
	).Scan(&scopedOrg); err != nil {
		return 0, 0, w.Wrapf(err, "failed to read tenant scope")
	}

	query := `SELECT public.organization_eligible_administrators($1)`
	if scopedOrg != orgID {
		query = `
			SELECT member.user_id
			FROM organization_members AS member
			JOIN users AS u ON u.uuid = member.user_id
			WHERE member.org_id = $1
			  AND member.role IN ('owner', 'admin')
			  AND u.status = 'active'`
	}

	rows, err := executor.Query(ctx, query, orgID)
	if err != nil {
		return 0, 0, w.Wrapf(err, "failed to count org administrators")
	}
	defer rows.Close()

	total, others := 0, 0
	for rows.Next() {
		var administrator string
		if err := rows.Scan(&administrator); err != nil {
			return 0, 0, w.Wrapf(err, "failed to scan org administrator")
		}
		total++
		if administrator != excludeUserID {
			others++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, w.Wrapf(err, "failed to read org administrators")
	}
	return total, others, nil
}

// LockOrgAdministration serializes every change to one organization's
// administrative standing — a role upsert, a demotion, or a removal, whichever
// member it names. It must be taken in the same transaction as the roster read
// the decision rests on and the membership write that follows; otherwise two
// transactions each observe the same administrators and each remove one.
//
// Lock order when a path takes more than one: LockOrgAdministration ->
// LockOrgMembership -> LockEntitlementQuota.
func (s *PostgresStore) LockOrgAdministration(ctx context.Context, orgID string) error {
	if _, ok := ctx.Value("tx").(pgx.Tx); !ok { //nolint:staticcheck // shared transaction context key
		return errors.New("org administration lock requires a tenant transaction")
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		business.OrgAdministrationLockKey(orgID),
	)
	if err != nil {
		return wool.Get(ctx).In("LockOrgAdministration").Wrapf(err, "failed to lock org administration")
	}
	return nil
}

// LockOrgMembership serializes every mutation of one (organization, user)
// authority pair — the organization membership row itself and the team
// memberships that depend on it. It must be taken in the same transaction as
// both the membership write and the dependent-access write; otherwise a team
// insert and an organization removal can interleave and leave a durable team
// row behind a departed member.
func (s *PostgresStore) LockOrgMembership(ctx context.Context, orgID string, userID string) error {
	if _, ok := ctx.Value("tx").(pgx.Tx); !ok { //nolint:staticcheck // shared transaction context key
		return errors.New("org membership lock requires a tenant transaction")
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"membership:"+orgID+":"+userID,
	)
	if err != nil {
		return wool.Get(ctx).In("LockOrgMembership").Wrapf(err, "failed to lock org membership")
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

func (s *PostgresStore) GetOrgMembership(ctx context.Context, orgID string, userID string) (*gen.OrgMembership, error) {
	w := wool.Get(ctx).In("GetOrgMembership")
	var membership *gen.OrgMembership
	load := func(ctx context.Context, executor ReadQueryExecutor, query string, args ...any) error {
		var value gen.OrgMembership
		var role string
		var joinedAt time.Time
		err := executor.QueryRow(ctx, query, args...).Scan(&value.OrgId, &value.UserId, &role, &joinedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return w.Wrapf(err, "failed to get org membership")
		}
		value.Role = parseOrgRole(role)
		value.JoinedAt = timestamppb.New(joinedAt)
		membership = &value
		return nil
	}

	if s.database != nil {
		err := s.readAs(ctx, orgID, userID, func(ctx context.Context, executor ReadQueryExecutor) error {
			return load(ctx, executor, `
				SELECT org_id, user_id, role, joined_at
				FROM organization_members
				WHERE org_id = current_setting('app.current_org_id', true)::uuid
				  AND user_id = current_setting('app.current_user_id', true)::uuid`)
		})
		return membership, err
	}

	// Local/test compatibility while the remaining repository is migrated.
	// Production never enters this branch: NewPostgresStore always installs
	// the service-postgres boundary.
	legacyRead := func(ctx context.Context) error {
		return load(ctx, s.getQueryExecutor(ctx), `
			SELECT org_id, user_id, role, joined_at
			FROM organization_members
			WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	}
	if _, hasTx := ctx.Value("tx").(pgx.Tx); hasTx { //nolint:staticcheck // legacy transaction bridge
		return membership, legacyRead(ctx)
	}
	err := s.WithOrgTx(ctx, orgID, legacyRead)
	return membership, err
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
