package business_test

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type notificationPreferenceStore struct {
	business.Store
	settings      *gen.UserSettings
	settingsErr   error
	notifications []*business.Notification
}

func (store *notificationPreferenceStore) WithUserTx(
	ctx context.Context,
	_ string,
	fn func(context.Context) error,
) error {
	return fn(ctx)
}

func (store *notificationPreferenceStore) GetUserSettings(
	_ context.Context,
	_ string,
) (*gen.UserSettings, error) {
	if store.settingsErr != nil {
		return nil, store.settingsErr
	}
	if store.settings == nil {
		return &gen.UserSettings{}, nil
	}
	return proto.Clone(store.settings).(*gen.UserSettings), nil
}

func (store *notificationPreferenceStore) CreateNotification(
	_ context.Context,
	notification *business.Notification,
) error {
	store.notifications = append(store.notifications, notification)
	return nil
}

func TestCreateNotificationUsesEnabledDefault(t *testing.T) {
	store := &notificationPreferenceStore{}
	service, err := business.NewService(store)
	require.NoError(t, err)

	notification, err := service.CreateNotification(
		context.Background(),
		"user-1",
		"org-1",
		"Invitation",
		"You have been invited",
		"info",
		"/invitations/accept?token=token",
	)

	require.NoError(t, err)
	require.NotNil(t, notification)
	require.Len(t, store.notifications, 1)
	require.Equal(t, "/invitations/accept?token=token", store.notifications[0].ActionURL)
}

func TestCreateNotificationSkipsUserOptOut(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Notifications.InApp.Set(settings, false))
	store := &notificationPreferenceStore{settings: settings}
	service, err := business.NewService(store)
	require.NoError(t, err)

	notification, err := service.CreateNotification(
		context.Background(),
		"user-1",
		"",
		"Optional update",
		"Body",
		"info",
		"",
	)

	require.NoError(t, err)
	require.Nil(t, notification)
	require.Empty(t, store.notifications)
}

func TestCreateNotificationDoesNotSuppressMandatoryCategories(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Notifications.InApp.Set(settings, false))

	for _, notificationType := range []string{"security", "billing"} {
		t.Run(notificationType, func(t *testing.T) {
			store := &notificationPreferenceStore{settings: settings}
			service, err := business.NewService(store)
			require.NoError(t, err)

			notification, err := service.CreateNotification(
				context.Background(),
				"user-1",
				"org-1",
				"Required update",
				"Body",
				notificationType,
				"/settings/security",
			)

			require.NoError(t, err)
			require.NotNil(t, notification)
			require.Len(t, store.notifications, 1)
		})
	}
}

func TestCreateNotificationFailsClosedWhenPreferenceCannotBeRead(t *testing.T) {
	store := &notificationPreferenceStore{settingsErr: errors.New("settings unavailable")}
	service, err := business.NewService(store)
	require.NoError(t, err)

	notification, err := service.CreateNotification(
		context.Background(),
		"user-1",
		"",
		"Optional update",
		"Body",
		"info",
		"",
	)

	require.Error(t, err)
	require.Nil(t, notification)
	require.Empty(t, store.notifications)
}
