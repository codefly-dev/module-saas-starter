package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// dashboardRow is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// letting one scanner serve the single-row and iteration paths.
type dashboardRow interface {
	Scan(dest ...any) error
}

func (s *PostgresStore) CreateDashboard(ctx context.Context, dashboard *business.Dashboard) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO dashboards (id, org_id, owner_id, name, spec, visibility)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)`,
		dashboard.ID, dashboard.OrgID, dashboard.OwnerID, dashboard.Name,
		dashboard.Spec, string(dashboard.Visibility))
	return err
}

func (s *PostgresStore) GetDashboard(ctx context.Context, id string) (*business.Dashboard, error) {
	q := s.getQueryExecutor(ctx)
	row := q.QueryRow(ctx, `
		SELECT id, org_id, owner_id, name, spec, visibility, created_at, updated_at
		FROM dashboards
		WHERE id = $1`, id)
	dashboard, err := scanDashboard(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return dashboard, nil
}

func (s *PostgresStore) ListDashboards(ctx context.Context, orgID, ownerID string, scope business.DashboardListScope) ([]*business.Dashboard, error) {
	q := s.getQueryExecutor(ctx)

	where := "org_id = $1 AND (owner_id = $2 OR visibility = 'org')"
	args := []any{orgID, ownerID}
	switch scope {
	case business.DashboardListMine:
		where = "org_id = $1 AND owner_id = $2"
	case business.DashboardListOrgShared:
		where = "org_id = $1 AND visibility = 'org'"
		args = []any{orgID}
	}

	rows, err := q.Query(ctx, `
		SELECT id, org_id, owner_id, name, spec, visibility, created_at, updated_at
		FROM dashboards
		WHERE `+where+`
		ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dashboards []*business.Dashboard
	for rows.Next() {
		dashboard, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		dashboards = append(dashboards, dashboard)
	}
	return dashboards, rows.Err()
}

func (s *PostgresStore) UpdateDashboard(ctx context.Context, id string, name *string, spec []byte) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE dashboards
		SET name = COALESCE($2, name),
		    spec = COALESCE($3::jsonb, spec),
		    updated_at = NOW()
		WHERE id = $1`, id, name, spec)
	return err
}

func (s *PostgresStore) DeleteDashboard(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `DELETE FROM dashboards WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) SetDashboardVisibility(ctx context.Context, id string, visibility business.DashboardVisibility) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE dashboards
		SET visibility = $2, updated_at = NOW()
		WHERE id = $1`, id, string(visibility))
	return err
}

func scanDashboard(row dashboardRow) (*business.Dashboard, error) {
	var (
		dashboard  business.Dashboard
		visibility string
	)
	if err := row.Scan(
		&dashboard.ID, &dashboard.OrgID, &dashboard.OwnerID, &dashboard.Name,
		&dashboard.Spec, &visibility, &dashboard.CreatedAt, &dashboard.UpdatedAt,
	); err != nil {
		return nil, err
	}
	dashboard.Visibility = business.DashboardVisibility(visibility)
	return &dashboard, nil
}
