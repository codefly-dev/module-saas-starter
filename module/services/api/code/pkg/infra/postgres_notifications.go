package infra

import (
	"context"
	"time"

	"api/pkg/business"
)

func (s *PostgresStore) CreateNotification(ctx context.Context, n *business.Notification) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO notifications (id, user_id, org_id, title, body, type, action_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		n.ID, n.UserID, nilIfEmpty(n.OrgID), n.Title, n.Body, n.Type, nilIfEmpty(n.ActionURL))
	return err
}

func (s *PostgresStore) ListNotifications(ctx context.Context, userID string, pageSize int, pageToken string) ([]*business.Notification, string, error) {
	q := s.getQueryExecutor(ctx)

	query := `
		SELECT id, user_id, org_id, title, body, type, action_url, read_at, created_at
		FROM notifications
		WHERE user_id = $1`
	args := []any{userID}

	if pageToken != "" {
		query += ` AND created_at < $2`
		args = append(args, pageToken)
		query += ` ORDER BY created_at DESC LIMIT $3`
		args = append(args, pageSize)
	} else {
		query += ` ORDER BY created_at DESC LIMIT $2`
		args = append(args, pageSize)
	}

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var notifications []*business.Notification
	for rows.Next() {
		var n business.Notification
		var orgID, actionURL *string
		var readAt *time.Time

		err := rows.Scan(&n.ID, &n.UserID, &orgID, &n.Title, &n.Body, &n.Type,
			&actionURL, &readAt, &n.CreatedAt)
		if err != nil {
			return nil, "", err
		}
		if orgID != nil {
			n.OrgID = *orgID
		}
		if actionURL != nil {
			n.ActionURL = *actionURL
		}
		n.ReadAt = readAt
		notifications = append(notifications, &n)
	}

	// Next page token is the created_at of the last item
	var nextToken string
	if len(notifications) == pageSize {
		nextToken = notifications[len(notifications)-1].CreatedAt.Format(time.RFC3339Nano)
	}

	return notifications, nextToken, nil
}

func (s *PostgresStore) GetUnreadCount(ctx context.Context, userID string) (int, error) {
	q := s.getQueryExecutor(ctx)
	var count int
	err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications
		WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&count)
	return count, err
}

func (s *PostgresStore) MarkNotificationRead(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE notifications SET read_at = NOW()
		WHERE id = $1 AND read_at IS NULL`, id)
	return err
}

func (s *PostgresStore) MarkAllNotificationsRead(ctx context.Context, userID string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE notifications SET read_at = NOW()
		WHERE user_id = $1 AND read_at IS NULL`, userID)
	return err
}

func (s *PostgresStore) DeleteNotification(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `DELETE FROM notifications WHERE id = $1`, id)
	return err
}

// GetNotificationUserID resolves notification.id → user_id. Called
// under WithBypass by Service.MarkRead / DeleteNotification before
// entering the owner's WithUserTx for the actual mutation. Returns
// ("", nil) on miss; caller decides whether that's a 404 or an
// authz failure.
func (s *PostgresStore) GetNotificationUserID(ctx context.Context, id string) (string, error) {
	q := s.getQueryExecutor(ctx)
	var userID string
	err := q.QueryRow(ctx, `SELECT user_id FROM notifications WHERE id = $1`, id).Scan(&userID)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return "", nil
		}
		return "", err
	}
	return userID, nil
}
