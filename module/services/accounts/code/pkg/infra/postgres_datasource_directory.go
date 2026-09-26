package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The datasource directory's rows (migration 8). Every statement runs inside
// the organization's transaction, so the tenant policy scopes it; the explicit
// org_id predicates say the same thing in the query.

func uniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (s *PostgresStore) UpsertDatasourceAccountLink(ctx context.Context, link *business.DatasourceAccountLink) (*business.DatasourceAccountLink, error) {
	q := s.getQueryExecutor(ctx)
	var existing business.DatasourceAccountLink
	err := q.QueryRow(ctx, `
		SELECT id::text, org_id::text, user_id::text, connector, provider_account_id, provider_account_login, created_at
		FROM datasource_account_links
		WHERE org_id = $1::uuid AND connector = $2 AND provider_account_id = $3`,
		link.OrgID, link.Connector, link.ProviderAccountID).Scan(
		&existing.ID, &existing.OrgID, &existing.UserID, &existing.Connector,
		&existing.ProviderAccountID, &existing.ProviderAccountLogin, &existing.CreatedAt)
	switch {
	case err == nil && existing.UserID != link.UserID:
		return nil, business.ErrDatasourceAccountLinkedElsewhere
	case err == nil:
		// The same person linking the same account again: keep the link, and
		// its current handle.
		if _, err := q.Exec(ctx, `UPDATE datasource_account_links SET provider_account_login = $2 WHERE id = $1::uuid`,
			existing.ID, link.ProviderAccountLogin); err != nil {
			return nil, err
		}
		existing.ProviderAccountLogin = link.ProviderAccountLogin
		return &existing, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	out := *link
	err = q.QueryRow(ctx, `
		INSERT INTO datasource_account_links (id, org_id, user_id, connector, provider_account_id, provider_account_login)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
		RETURNING created_at`,
		link.ID, link.OrgID, link.UserID, link.Connector, link.ProviderAccountID, link.ProviderAccountLogin).Scan(&out.CreatedAt)
	if uniqueViolation(err, "datasource_account_links_account_key") {
		// A concurrent link of the same account won; whoever holds it now, this
		// one did not land.
		return nil, business.ErrDatasourceAccountLinkedElsewhere
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func scanAccountLinks(rows pgx.Rows) ([]*business.DatasourceAccountLink, error) {
	defer rows.Close()
	var out []*business.DatasourceAccountLink
	for rows.Next() {
		var l business.DatasourceAccountLink
		if err := rows.Scan(&l.ID, &l.OrgID, &l.UserID, &l.Connector, &l.ProviderAccountID, &l.ProviderAccountLogin, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListDatasourceAccountLinks(ctx context.Context, orgID, userID string) ([]*business.DatasourceAccountLink, error) {
	var user any
	if userID != "" {
		user = userID
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT id::text, org_id::text, user_id::text, connector, provider_account_id, provider_account_login, created_at
		FROM datasource_account_links
		WHERE org_id = $1::uuid AND ($2::uuid IS NULL OR user_id = $2::uuid)
		ORDER BY created_at, id`, orgID, user)
	if err != nil {
		return nil, err
	}
	return scanAccountLinks(rows)
}

func (s *PostgresStore) GetDatasourceAccountLink(ctx context.Context, orgID, id string) (*business.DatasourceAccountLink, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT id::text, org_id::text, user_id::text, connector, provider_account_id, provider_account_login, created_at
		FROM datasource_account_links
		WHERE org_id = $1::uuid AND id = $2::uuid`, orgID, id)
	if err != nil {
		return nil, err
	}
	links, err := scanAccountLinks(rows)
	if err != nil || len(links) == 0 {
		return nil, err
	}
	return links[0], nil
}

func (s *PostgresStore) DeleteDatasourceAccountLink(ctx context.Context, orgID, id string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `DELETE FROM datasource_account_links WHERE org_id = $1::uuid AND id = $2::uuid`, orgID, id)
	return err
}

func (s *PostgresStore) InsertDatasourceGroupBinding(ctx context.Context, b *business.DatasourceGroupBinding) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO datasource_group_bindings (id, org_id, connector, provider_group_id, team_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, NULLIF($6, '')::uuid)`,
		b.ID, b.OrgID, b.Connector, b.ProviderGroupID, b.TeamID, b.CreatedBy)
	if uniqueViolation(err, "datasource_group_bindings_group_key") {
		return business.ErrDatasourceGroupAlreadyBound
	}
	return err
}

const groupBindingColumns = `id::text, org_id::text, connector, provider_group_id, team_id::text, COALESCE(created_by::text, ''), created_at`

func scanGroupBinding(row pgx.Row) (*business.DatasourceGroupBinding, error) {
	var b business.DatasourceGroupBinding
	if err := row.Scan(&b.ID, &b.OrgID, &b.Connector, &b.ProviderGroupID, &b.TeamID, &b.CreatedBy, &b.CreatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *PostgresStore) ListDatasourceGroupBindings(ctx context.Context, orgID string) ([]*business.DatasourceGroupBinding, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+groupBindingColumns+`
		FROM datasource_group_bindings
		WHERE org_id = $1::uuid
		ORDER BY connector, provider_group_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*business.DatasourceGroupBinding
	for rows.Next() {
		b, err := scanGroupBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteDatasourceGroupBinding(ctx context.Context, orgID, id string) (*business.DatasourceGroupBinding, error) {
	b, err := scanGroupBinding(s.getQueryExecutor(ctx).QueryRow(ctx, `
		DELETE FROM datasource_group_bindings
		WHERE org_id = $1::uuid AND id = $2::uuid
		RETURNING `+groupBindingColumns, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

const domainColumns = `id::text, org_id::text, domain, verification_token, verified_at, COALESCE(created_by::text, ''), created_at`

func scanDomain(row pgx.Row) (*business.DatasourceDomain, error) {
	var d business.DatasourceDomain
	if err := row.Scan(&d.ID, &d.OrgID, &d.Domain, &d.VerificationToken, &d.VerifiedAt, &d.CreatedBy, &d.CreatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *PostgresStore) InsertDatasourceDomain(ctx context.Context, d *business.DatasourceDomain) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO datasource_domains (id, org_id, domain, verification_token, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, '')::uuid)`,
		d.ID, d.OrgID, d.Domain, d.VerificationToken, d.CreatedBy)
	if uniqueViolation(err, "datasource_domains_org_domain_key") {
		return business.ErrDatasourceDomainAlreadyClaimed
	}
	return err
}

func (s *PostgresStore) ListDatasourceDomains(ctx context.Context, orgID string) ([]*business.DatasourceDomain, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT `+domainColumns+` FROM datasource_domains WHERE org_id = $1::uuid ORDER BY domain`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*business.DatasourceDomain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetDatasourceDomain(ctx context.Context, orgID, id string) (*business.DatasourceDomain, error) {
	d, err := scanDomain(s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT `+domainColumns+` FROM datasource_domains WHERE org_id = $1::uuid AND id = $2::uuid`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *PostgresStore) MarkDatasourceDomainVerified(ctx context.Context, orgID, id string, at time.Time) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE datasource_domains SET verified_at = $3
		WHERE org_id = $1::uuid AND id = $2::uuid AND verified_at IS NULL`, orgID, id, at)
	return err
}

func (s *PostgresStore) DeleteDatasourceDomain(ctx context.Context, orgID, id string) (*business.DatasourceDomain, error) {
	d, err := scanDomain(s.getQueryExecutor(ctx).QueryRow(ctx, `
		DELETE FROM datasource_domains WHERE org_id = $1::uuid AND id = $2::uuid
		RETURNING `+domainColumns, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *PostgresStore) LinkedDatasourceUsers(ctx context.Context, orgID, connectorKey string, accountIDs []string) (map[string]string, error) {
	return s.stringMapping(ctx, `
		SELECT provider_account_id, user_id::text FROM datasource_account_links
		WHERE org_id = $1::uuid AND connector = $2 AND provider_account_id = ANY($3)`, orgID, connectorKey, accountIDs)
}

func (s *PostgresStore) BoundDatasourceTeams(ctx context.Context, orgID, connectorKey string, groupIDs []string) (map[string]string, error) {
	return s.stringMapping(ctx, `
		SELECT provider_group_id, team_id::text FROM datasource_group_bindings
		WHERE org_id = $1::uuid AND connector = $2 AND provider_group_id = ANY($3)`, orgID, connectorKey, groupIDs)
}

func (s *PostgresStore) stringMapping(ctx context.Context, query, orgID, connectorKey string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, query, orgID, connectorKey, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *PostgresStore) VerifiedDatasourceDomains(ctx context.Context, orgID string, domains []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(domains) == 0 {
		return out, nil
	}
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT domain FROM datasource_domains
		WHERE org_id = $1::uuid AND verified_at IS NOT NULL AND domain = ANY($2)`, orgID, domains)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// SpendDatasourceBudget meters one operation of a provider credential. It runs
// on the control plane; the row lock serializes every replica spending the same
// credential.
func (s *PostgresStore) SpendDatasourceBudget(ctx context.Context, key string, limit int, window time.Duration, now time.Time) (bool, time.Time, time.Time, error) {
	q := s.getQueryExecutor(ctx)
	if _, err := q.Exec(ctx, `
		INSERT INTO datasource_credential_budgets (credential_key, window_started_at, used, updated_at)
		VALUES ($1, $2, 0, $2)
		ON CONFLICT (credential_key) DO NOTHING`, key, now); err != nil {
		return false, time.Time{}, time.Time{}, err
	}
	var started time.Time
	var used int
	var blocked *time.Time
	if err := q.QueryRow(ctx, `
		SELECT window_started_at, used, blocked_until FROM datasource_credential_budgets
		WHERE credential_key = $1 FOR UPDATE`, key).Scan(&started, &used, &blocked); err != nil {
		return false, time.Time{}, time.Time{}, err
	}
	if blocked != nil && now.Before(*blocked) {
		return false, started.Add(window), *blocked, nil
	}
	if !now.Before(started.Add(window)) {
		started, used = now, 0
	}
	reset := started.Add(window)
	if used >= limit {
		_, err := q.Exec(ctx, `
			UPDATE datasource_credential_budgets
			SET window_started_at = $2, used = $3, blocked_until = NULL, updated_at = $4
			WHERE credential_key = $1`, key, started, used, now)
		return false, reset, time.Time{}, err
	}
	_, err := q.Exec(ctx, `
		UPDATE datasource_credential_budgets
		SET window_started_at = $2, used = $3, blocked_until = NULL, updated_at = $4
		WHERE credential_key = $1`, key, started, used+1, now)
	return err == nil, reset, time.Time{}, err
}

// BlockDatasourceBudget records a provider's own rate limit on the credential.
// A later block never shortens an earlier one.
func (s *PostgresStore) BlockDatasourceBudget(ctx context.Context, key string, until time.Time) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO datasource_credential_budgets (credential_key, window_started_at, used, blocked_until, updated_at)
		VALUES ($1, now(), 0, $2, now())
		ON CONFLICT (credential_key) DO UPDATE
		SET blocked_until = GREATEST(COALESCE(datasource_credential_budgets.blocked_until, $2), $2), updated_at = now()`,
		key, until)
	return err
}
