package infra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"api/pkg/business"
)

// Compile-time check that PostgresStore implements GDPRStore.
var _ business.GDPRStore = (*PostgresStore)(nil)

func (s *PostgresStore) CreateGDPRRequest(ctx context.Context, req *business.GDPRRequest) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		INSERT INTO gdpr_requests (id, user_id, type, status, download_url, expires_at, error, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		req.ID, req.UserID, string(req.Type), string(req.Status),
		req.DownloadURL, req.ExpiresAt, req.Error, req.CompletedAt)
	return err
}

func (s *PostgresStore) GetGDPRRequest(ctx context.Context, id string) (*business.GDPRRequest, error) {
	q := s.getQueryExecutor(ctx)

	row := q.QueryRow(ctx, `
		SELECT id, user_id, type, status, download_url, expires_at, error, created_at, completed_at
		FROM gdpr_requests WHERE id = $1`, id)

	var req business.GDPRRequest
	var reqType, status string
	var downloadURL *string
	var expiresAt *time.Time
	var errMsg *string
	var completedAt *time.Time

	err := row.Scan(&req.ID, &req.UserID, &reqType, &status,
		&downloadURL, &expiresAt, &errMsg, &req.CreatedAt, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("GDPR request not found"), business.ErrTypeNotFound)
		}
		return nil, err
	}

	req.Type = business.GDPRRequestType(reqType)
	req.Status = business.GDPRRequestStatus(status)
	if downloadURL != nil {
		req.DownloadURL = *downloadURL
	}
	req.ExpiresAt = expiresAt
	if errMsg != nil {
		req.Error = *errMsg
	}
	req.CompletedAt = completedAt

	return &req, nil
}

func (s *PostgresStore) UpdateGDPRRequest(ctx context.Context, req *business.GDPRRequest) error {
	q := s.getQueryExecutor(ctx)

	_, err := q.Exec(ctx, `
		UPDATE gdpr_requests
		SET status = $2, download_url = $3, expires_at = $4, error = $5, completed_at = $6
		WHERE id = $1`,
		req.ID, string(req.Status), req.DownloadURL, req.ExpiresAt, req.Error, req.CompletedAt)
	return err
}

func (s *PostgresStore) GetUserGDPRRequests(ctx context.Context, userID string) ([]*business.GDPRRequest, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT id, user_id, type, status, download_url, expires_at, error, created_at, completed_at
		FROM gdpr_requests WHERE user_id = $1
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*business.GDPRRequest
	for rows.Next() {
		var req business.GDPRRequest
		var reqType, status string
		var downloadURL *string
		var expiresAt *time.Time
		var errMsg *string
		var completedAt *time.Time

		if err := rows.Scan(&req.ID, &req.UserID, &reqType, &status,
			&downloadURL, &expiresAt, &errMsg, &req.CreatedAt, &completedAt); err != nil {
			return nil, err
		}

		req.Type = business.GDPRRequestType(reqType)
		req.Status = business.GDPRRequestStatus(status)
		if downloadURL != nil {
			req.DownloadURL = *downloadURL
		}
		req.ExpiresAt = expiresAt
		if errMsg != nil {
			req.Error = *errMsg
		}
		req.CompletedAt = completedAt

		requests = append(requests, &req)
	}

	return requests, nil
}
