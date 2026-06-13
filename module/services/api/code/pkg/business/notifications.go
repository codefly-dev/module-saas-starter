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

// CreateNotification creates and stores a new notification for a
// user. notifications is RLS-protected by user_id (Phase 2G); the
// write runs inside WithUserTx scoped to the recipient. Callers from
// system paths (no actor) should still pass a userID — the recipient
// is the right scope here.
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

	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		return s.store.CreateNotification(ctx, n)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create notification")
	}

	return n, nil
}

// ListNotifications returns paginated notifications for a user.
// Wrapped in WithUserTx so the RLS policy on `notifications` filters
// to userID. Without the wrap the SQL still has WHERE user_id = $1
// but returns zero rows under app_tenant + no GUC — fail-closed.
func (s *Service) ListNotifications(ctx context.Context, userID string, pageSize int, pageToken string) ([]*Notification, string, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	var notifs []*Notification
	var next string
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		ns, nt, err := s.store.ListNotifications(ctx, userID, pageSize, pageToken)
		notifs, next = ns, nt
		return err
	}); err != nil {
		return nil, "", err
	}
	return notifs, next, nil
}

// GetUnreadCount returns the number of unread notifications for a user.
func (s *Service) GetUnreadCount(ctx context.Context, userID string) (int, error) {
	var count int
	if err := s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		c, err := s.store.GetUnreadCount(ctx, userID)
		count = c
		return err
	}); err != nil {
		return 0, err
	}
	return count, nil
}

// MarkRead marks a single notification as read. The id-only signature
// forces a bypass-then-WithUserTx dance; we read the notif's user_id
// under bypass first, then enter the user's tx for the update.
// Handler-level authz (caller must be the owning user OR a platform
// admin) gates who can ask.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	userID, err := s.resolveNotificationUser(ctx, id)
	if err != nil {
		return err
	}
	return s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		return s.store.MarkNotificationRead(ctx, id)
	})
}

// MarkAllRead marks all notifications as read for a user.
func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	return s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		return s.store.MarkAllNotificationsRead(ctx, userID)
	})
}

// DeleteNotification deletes a single notification. Same id-only
// bypass-resolve pattern as MarkRead.
func (s *Service) DeleteNotification(ctx context.Context, id string) error {
	userID, err := s.resolveNotificationUser(ctx, id)
	if err != nil {
		return err
	}
	return s.store.WithUserTx(ctx, userID, func(ctx context.Context) error {
		return s.store.DeleteNotification(ctx, id)
	})
}

// resolveNotificationUser looks up the user_id for a notification
// id. The lookup runs under WithBypass — it's a per-request resolve
// before entering the user-scoped tx, parallel to resolveTeamOrg in
// teams.go. Handler authz gates the actual permission decision.
//
// Returns "" + error when the row doesn't exist, so a probe with a
// random id can't be used as an existence oracle.
func (s *Service) resolveNotificationUser(ctx context.Context, id string) (string, error) {
	var userID string
	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		u, err := s.store.GetNotificationUserID(ctx, id)
		userID = u
		return err
	}); err != nil {
		return "", err
	}
	if userID == "" {
		return "", wool.Get(ctx).NewError("notification not found")
	}
	return userID, nil
}

// NotifyUser is a convenience helper for internal code to create a
// simple info notification for a user without specifying all fields.
func (s *Service) NotifyUser(ctx context.Context, userID, title, body string) error {
	_, err := s.CreateNotification(ctx, userID, "", title, body, "info", "")
	return err
}
