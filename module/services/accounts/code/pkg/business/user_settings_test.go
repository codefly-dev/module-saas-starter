package business_test

import (
	"context"
	"sync"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type settingsFakeStore struct {
	business.Store
	mu       sync.Mutex
	settings map[string]*gen.UserSettings
}

type settingsFakeScoped struct {
	business.Scoped
	identity business.Identity
}

func (scope *settingsFakeScoped) Within(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	return fn(ctx)
}

func (scope *settingsFakeScoped) Identity() business.Identity { return scope.identity }

func newSettingsFakeStore() *settingsFakeStore {
	return &settingsFakeStore{settings: map[string]*gen.UserSettings{}}
}

func (store *settingsFakeStore) GetUserSettings(
	_ context.Context,
	userID string,
) (*gen.UserSettings, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if value := store.settings[userID]; value != nil {
		return proto.Clone(value).(*gen.UserSettings), nil
	}
	return &gen.UserSettings{}, nil
}

func (store *settingsFakeStore) UpdateUserSettings(
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

func (store *settingsFakeStore) As(identity business.Identity) business.Scoped {
	return &settingsFakeScoped{identity: identity}
}

func TestGetUserSettingsEmptyRowReturnsTypedDocument(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())

	got, err := service.GetUserSettings(context.Background(), "user-1")

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, got.Appearance.GetTheme())
	require.Equal(t, "en", got.Regional.GetLocale())
	require.True(t, got.Email.GetProduct())
	require.True(t, got.Notifications.GetInApp())
}

func TestUpdateUserSettingsPartialPatchPreservesTopLevelValues(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())
	ctx := context.Background()

	seed := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Regional.Locale.Set(seed, "fr"))
	_, err := service.UpdateUserSettings(ctx, "user-1", seed, nil)
	require.NoError(t, err)

	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		patch,
		gen.ThemePreference_THEME_PREFERENCE_DARK,
	))
	got, err := service.UpdateUserSettings(ctx, "user-1", patch, nil)

	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_DARK, got.Appearance.GetTheme())
	require.Equal(t, "fr", got.Regional.GetLocale())
}

func TestUpdateUserSettingsNestedPatchPreservesSiblings(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())
	ctx := context.Background()

	seed := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Email.Product.Set(seed, true))
	require.NoError(t, usersettings.Fields.Email.Marketing.Set(seed, true))
	_, err := service.UpdateUserSettings(ctx, "user-1", seed, nil)
	require.NoError(t, err)

	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Email.Product.Set(patch, false))
	got, err := service.UpdateUserSettings(ctx, "user-1", patch, nil)

	require.NoError(t, err)
	require.NotNil(t, got.Email.Product)
	require.False(t, got.Email.GetProduct())
	require.NotNil(t, got.Email.Marketing)
	require.True(t, got.Email.GetMarketing())
}

func TestUpdateUserSettingsPreservesExplicitFalsePresence(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())
	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Notifications.InApp.Set(patch, false))

	got, err := service.UpdateUserSettings(context.Background(), "user-1", patch, nil)

	require.NoError(t, err)
	value, present, err := usersettings.Fields.Notifications.InApp.Lookup(got)
	require.NoError(t, err)
	require.True(t, present)
	require.False(t, value)
}

func TestUpdateUserSettingsRejectsInvalidTheme(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())
	invalid := gen.ThemePreference(999)

	_, err := service.UpdateUserSettings(
		context.Background(),
		"user-1",
		&gen.UserSettings{
			Appearance: &gen.UserAppearanceSettings{Theme: &invalid},
		},
		nil,
	)

	require.Error(t, err)
}

func TestUpdateUserSettingsRejectsDisablingSecurityEmail(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())
	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Email.Security.Set(patch, false))

	_, err := service.UpdateUserSettings(
		context.Background(),
		"user-1",
		patch,
		nil,
	)

	require.Error(t, err)
}

func TestUpdateUserSettingsResetReturnsDefaultAndPrunesLastParent(t *testing.T) {
	store := newSettingsFakeStore()
	service := newSettingsService(store)
	ctx := context.Background()
	seed := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		seed,
		gen.ThemePreference_THEME_PREFERENCE_DARK,
	))
	_, err := service.UpdateUserSettings(ctx, "user-1", seed, nil)
	require.NoError(t, err)

	got, err := service.UpdateUserSettings(
		ctx,
		"user-1",
		nil,
		[]string{usersettings.Fields.Appearance.Theme.Path()},
	)

	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, got.Appearance.GetTheme())
	require.Nil(t, store.settings["user-1"].Appearance)
}

func TestUpdateUserSettingsRejectsPatchAndResetOfSamePath(t *testing.T) {
	service := newSettingsService(newSettingsFakeStore())
	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Email.Product.Set(patch, false))

	_, err := service.UpdateUserSettings(
		context.Background(),
		"user-1",
		patch,
		[]string{usersettings.Fields.Email.Product.Path()},
	)

	require.Error(t, err)
}

func newSettingsService(store business.Store) *business.Service {
	service, _ := business.NewService(store)
	return service
}
