package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"
)

// Notification is the domain representation of a user notification.
type Notification struct {
	ID        string
	UserID    string
	OrgID     string
	Title     string
	Body      string
	Type      string // "info", "warning", "success", "error"
	ActionURL string
	ReadAt    *time.Time
	CreatedAt time.Time
}

// CreateNotification creates and stores a new notification for a user.
func (s *Service) CreateNotification(ctx context.Context, userID, orgID, title, body, notifType, actionURL string) (*Notification, error) {
	w := wool.Get(ctx).In("CreateNotification")

	if notifType == "" {
		notifType = "info"
	}

	n := &Notification{
		ID:        NewIDString(),
		UserID:    userID,
		OrgID:     orgID,
		Title:     title,
		Body:      body,
		Type:      notifType,
		ActionURL: actionURL,
	}

	if err := s.store.CreateNotification(ctx, n); err != nil {
		return nil, w.Wrapf(err, "cannot create notification")
	}

	return n, nil
}

// ListNotifications returns paginated notifications for a user.
func (s *Service) ListNotifications(ctx context.Context, userID string, pageSize int, pageToken string) ([]*Notification, string, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	return s.store.ListNotifications(ctx, userID, pageSize, pageToken)
}

// GetUnreadCount returns the number of unread notifications for a user.
func (s *Service) GetUnreadCount(ctx context.Context, userID string) (int, error) {
	return s.store.GetUnreadCount(ctx, userID)
}

// MarkRead marks a single notification as read.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	return s.store.MarkNotificationRead(ctx, id)
}

// MarkAllRead marks all notifications as read for a user.
func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	return s.store.MarkAllNotificationsRead(ctx, userID)
}

// DeleteNotification deletes a single notification.
func (s *Service) DeleteNotification(ctx context.Context, id string) error {
	return s.store.DeleteNotification(ctx, id)
}

// NotifyUser is a convenience helper for internal code to create a simple
// info notification for a user without needing to specify all fields.
func (s *Service) NotifyUser(ctx context.Context, userID, title, body string) error {
	_, err := s.CreateNotification(ctx, userID, "", title, body, "info", "")
	return err
}
