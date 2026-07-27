package adapters

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"
)

type userSettingsHandlerStore struct {
	business.Store
	mu         sync.Mutex
	settings   map[string]*gen.UserSettings
	identities []business.Identity
}

type userSettingsHandlerScope struct {
	business.Scoped
	identity business.Identity
}

func (scope *userSettingsHandlerScope) Within(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	return fn(ctx)
}

func (scope *userSettingsHandlerScope) Identity() business.Identity {
	return scope.identity
}

func newUserSettingsHandler(t *testing.T) (*userSettingsConnectHandler, *userSettingsHandlerStore) {
	t.Helper()
	store := &userSettingsHandlerStore{settings: make(map[string]*gen.UserSettings)}
	service, err := business.NewService(store)
	require.NoError(t, err)
	return &userSettingsConnectHandler{svc: service}, store
}

func (store *userSettingsHandlerStore) As(identity business.Identity) business.Scoped {
	store.mu.Lock()
	store.identities = append(store.identities, identity)
	store.mu.Unlock()
	return &userSettingsHandlerScope{identity: identity}
}

func (store *userSettingsHandlerStore) GetUserSettings(
	_ context.Context,
	userID string,
) (*gen.UserSettings, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if settings := store.settings[userID]; settings != nil {
		return proto.Clone(settings).(*gen.UserSettings), nil
	}
	return &gen.UserSettings{}, nil
}

func (store *userSettingsHandlerStore) UpdateUserSettings(
	_ context.Context,
	userID string,
	patch *gen.UserSettings,
	resetPaths []string,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.settings[userID]
	if current == nil {
		current = &gen.UserSettings{}
	}
	if err := usersettings.ApplyResets(current, resetPaths); err != nil {
		return err
	}
	proto.Merge(current, patch)
	store.settings[userID] = current
	return nil
}

func authenticatedSettingsRequest[T any](message *T) *connect.Request[T] {
	request := connect.NewRequest(message)
	request.Header().Set("X-Auth-Id", "user-1")
	return request
}

func TestUserSettingsConnectGetResolvesDefaultsForAuthenticatedCaller(t *testing.T) {
	handler, store := newUserSettingsHandler(t)

	response, err := handler.Get(
		context.Background(),
		authenticatedSettingsRequest(&gen.GetUserSettingsRequest{}),
	)

	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, response.Msg.Appearance.GetTheme())
	require.Equal(t, "en", response.Msg.Regional.GetLocale())
	require.Equal(t, []business.Identity{{UserID: "user-1"}}, store.identities)
}

func TestUserSettingsConnectUpdateAppliesTypedPatchAndClearMask(t *testing.T) {
	handler, store := newUserSettingsHandler(t)
	seed := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		seed,
		gen.ThemePreference_THEME_PREFERENCE_LIGHT,
	))
	require.NoError(t, usersettings.Fields.Regional.Locale.Set(seed, "fr"))
	store.settings["user-1"] = seed

	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		patch,
		gen.ThemePreference_THEME_PREFERENCE_DARK,
	))
	response, err := handler.Update(
		context.Background(),
		authenticatedSettingsRequest(&gen.UpdateUserSettingsRequest{
			Patch: patch,
			ClearMask: &fieldmaskpb.FieldMask{
				Paths: []string{usersettings.Fields.Regional.Locale.Path()},
			},
		}),
	)

	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_DARK, response.Msg.Appearance.GetTheme())
	require.Equal(t, "en", response.Msg.Regional.GetLocale())
	require.Nil(t, store.settings["user-1"].Regional)
}

func TestUserSettingsConnectRequiresAuthenticatedCaller(t *testing.T) {
	handler, _ := newUserSettingsHandler(t)

	_, err := handler.Get(
		context.Background(),
		connect.NewRequest(&gen.GetUserSettingsRequest{}),
	)

	require.Error(t, err)
	var connectError *connect.Error
	require.True(t, errors.As(err, &connectError))
	require.Equal(t, connect.CodeUnauthenticated, connectError.Code())
}
