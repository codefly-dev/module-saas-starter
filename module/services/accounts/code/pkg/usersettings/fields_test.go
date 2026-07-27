package usersettings_test

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDefaultsDoNotMaterializeMissingParents(t *testing.T) {
	settings := &gen.UserSettings{}

	theme, err := usersettings.Fields.Appearance.Theme.Get(settings)
	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, theme)
	require.Nil(t, settings.Appearance)

	locale, err := usersettings.Fields.Regional.Locale.Get(settings)
	require.NoError(t, err)
	require.Equal(t, "en", locale)
	require.Nil(t, settings.Regional)

	product, err := usersettings.Fields.Email.Product.Get(settings)
	require.NoError(t, err)
	require.True(t, product)
	require.Nil(t, settings.Email)

	inApp, err := usersettings.Fields.Notifications.InApp.Get(settings)
	require.NoError(t, err)
	require.True(t, inApp)
	require.Nil(t, settings.Notifications)
}

func TestNestedSetMaterializesParentAndPreservesExplicitFalse(t *testing.T) {
	settings := &gen.UserSettings{}

	require.NoError(t, usersettings.Fields.Email.Product.Set(settings, false))

	require.NotNil(t, settings.Email)
	require.NotNil(t, settings.Email.Product)
	require.False(t, settings.Email.GetProduct())
	value, present, err := usersettings.Fields.Email.Product.Lookup(settings)
	require.NoError(t, err)
	require.True(t, present)
	require.False(t, value)
}

func TestResolveMaterializesAllCommonDefaultsWithoutMutatingStored(t *testing.T) {
	stored := &gen.UserSettings{}

	resolved, err := usersettings.Resolve(stored)

	require.NoError(t, err)
	require.Nil(t, stored.Appearance)
	require.Nil(t, stored.Regional)
	require.Nil(t, stored.Email)
	require.Nil(t, stored.Notifications)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, resolved.Appearance.GetTheme())
	require.Equal(t, "en", resolved.Regional.GetLocale())
	require.Equal(t, "UTC", resolved.Regional.GetTimezone())
	require.Equal(t, "iso", resolved.Regional.GetDateFormat())
	require.Equal(t, "24h", resolved.Regional.GetTimeFormat())
	require.True(t, resolved.Email.GetProduct())
	require.False(t, resolved.Email.GetMarketing())
	require.True(t, resolved.Email.GetSecurity())
	require.True(t, resolved.Email.GetWeeklyDigest())
	require.True(t, resolved.Notifications.GetInApp())
	require.False(t, resolved.Notifications.GetPush())
	require.False(t, resolved.Notifications.GetSound())
}

func TestResolvePreservesExplicitFalseInsteadOfApplyingDefault(t *testing.T) {
	stored := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Email.Product.Set(stored, false))

	resolved, err := usersettings.Resolve(stored)

	require.NoError(t, err)
	require.NotNil(t, resolved.Email.Product)
	require.False(t, resolved.Email.GetProduct())
}

func TestResolvePreservesEveryExplicitZeroAndEmptyValue(t *testing.T) {
	stored := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		stored,
		gen.ThemePreference_THEME_PREFERENCE_SYSTEM,
	))
	require.NoError(t, usersettings.Fields.Regional.Locale.Set(stored, ""))
	require.NoError(t, usersettings.Fields.Regional.Timezone.Set(stored, ""))
	require.NoError(t, usersettings.Fields.Regional.DateFormat.Set(stored, ""))
	require.NoError(t, usersettings.Fields.Regional.TimeFormat.Set(stored, ""))
	require.NoError(t, usersettings.Fields.Email.Product.Set(stored, false))
	require.NoError(t, usersettings.Fields.Email.Marketing.Set(stored, false))
	require.NoError(t, usersettings.Fields.Email.Security.Set(stored, false))
	require.NoError(t, usersettings.Fields.Email.WeeklyDigest.Set(stored, false))
	require.NoError(t, usersettings.Fields.Notifications.InApp.Set(stored, false))
	require.NoError(t, usersettings.Fields.Notifications.Push.Set(stored, false))
	require.NoError(t, usersettings.Fields.Notifications.Sound.Set(stored, false))
	before := proto.Clone(stored)

	resolved, err := usersettings.Resolve(stored)

	require.NoError(t, err)
	require.True(t, proto.Equal(before, stored), "Resolve must never mutate sparse storage")
	require.NotSame(t, stored, resolved)
	require.NotNil(t, resolved.Appearance.Theme)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, resolved.Appearance.GetTheme())
	require.NotNil(t, resolved.Regional.Locale)
	require.Empty(t, resolved.Regional.GetLocale())
	require.NotNil(t, resolved.Regional.Timezone)
	require.Empty(t, resolved.Regional.GetTimezone())
	require.NotNil(t, resolved.Regional.DateFormat)
	require.Empty(t, resolved.Regional.GetDateFormat())
	require.NotNil(t, resolved.Regional.TimeFormat)
	require.Empty(t, resolved.Regional.GetTimeFormat())
	require.NotNil(t, resolved.Email.Product)
	require.False(t, resolved.Email.GetProduct())
	require.NotNil(t, resolved.Email.Marketing)
	require.False(t, resolved.Email.GetMarketing())
	require.NotNil(t, resolved.Email.Security)
	require.False(t, resolved.Email.GetSecurity())
	require.NotNil(t, resolved.Email.WeeklyDigest)
	require.False(t, resolved.Email.GetWeeklyDigest())
	require.NotNil(t, resolved.Notifications.InApp)
	require.False(t, resolved.Notifications.GetInApp())
	require.NotNil(t, resolved.Notifications.Push)
	require.False(t, resolved.Notifications.GetPush())
	require.NotNil(t, resolved.Notifications.Sound)
	require.False(t, resolved.Notifications.GetSound())
}

func TestResolveNilDocumentMaterializesTheCompleteCatalog(t *testing.T) {
	resolved, err := usersettings.Resolve(nil)

	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, resolved.Appearance.GetTheme())
	require.Equal(t, "en", resolved.Regional.GetLocale())
	require.Equal(t, "UTC", resolved.Regional.GetTimezone())
	require.Equal(t, "iso", resolved.Regional.GetDateFormat())
	require.Equal(t, "24h", resolved.Regional.GetTimeFormat())
	require.True(t, resolved.Email.GetProduct())
	require.False(t, resolved.Email.GetMarketing())
	require.True(t, resolved.Email.GetSecurity())
	require.True(t, resolved.Email.GetWeeklyDigest())
	require.True(t, resolved.Notifications.GetInApp())
	require.False(t, resolved.Notifications.GetPush())
	require.False(t, resolved.Notifications.GetSound())
}

func TestResolveFillsPresentButEmptyNestedParent(t *testing.T) {
	stored := &gen.UserSettings{
		Appearance: &gen.UserAppearanceSettings{},
		Regional:   &gen.UserRegionalSettings{},
	}

	resolved, err := usersettings.Resolve(stored)

	require.NoError(t, err)
	require.Nil(t, stored.Appearance.Theme)
	require.Nil(t, stored.Regional.Locale)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, resolved.Appearance.GetTheme())
	require.Equal(t, "en", resolved.Regional.GetLocale())
}

func TestClearPrunesOnlyEmptyParents(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Email.Product.Set(settings, false))
	require.NoError(t, usersettings.Fields.Email.Marketing.Set(settings, true))

	require.NoError(t, usersettings.Fields.Email.Product.Clear(settings))
	require.NotNil(t, settings.Email)
	require.True(t, settings.Email.GetMarketing())

	require.NoError(t, usersettings.Fields.Email.Marketing.Clear(settings))
	require.Nil(t, settings.Email)
}

func TestProtoJSONRoundTripIsTypedAndUsesProtoFieldNames(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		settings,
		gen.ThemePreference_THEME_PREFERENCE_DARK,
	))
	require.NoError(t, usersettings.Fields.Notifications.InApp.Set(settings, false))

	encoded, err := usersettings.JSON.Marshal(settings)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"appearance": {"theme": "THEME_PREFERENCE_DARK"},
		"notifications": {"in_app": false}
	}`, string(encoded))

	decoded, err := usersettings.JSON.Unmarshal(encoded)
	require.NoError(t, err)
	theme, err := usersettings.Fields.Appearance.Theme.Get(decoded)
	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_DARK, theme)
	inApp, present, err := usersettings.Fields.Notifications.InApp.Lookup(decoded)
	require.NoError(t, err)
	require.True(t, present)
	require.False(t, inApp)
}

func TestResetValidationAndApplication(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		settings,
		gen.ThemePreference_THEME_PREFERENCE_DARK,
	))
	path := usersettings.Fields.Appearance.Theme.Path()
	require.NoError(t, usersettings.ValidateResetPaths(&gen.UserSettings{}, []string{path}))
	require.NoError(t, usersettings.ApplyResets(settings, []string{path}))
	require.Nil(t, settings.Appearance)

	theme, err := usersettings.Fields.Appearance.Theme.Get(settings)
	require.NoError(t, err)
	require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, theme)
}

func TestResetValidationRejectsUnknownDuplicateAndPatchConflict(t *testing.T) {
	path := usersettings.Fields.Appearance.Theme.Path()
	require.Error(t, usersettings.ValidateResetPaths(
		&gen.UserSettings{},
		[]string{"appearance.unknown"},
	))
	require.Error(t, usersettings.ValidateResetPaths(
		&gen.UserSettings{},
		[]string{path, path},
	))

	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
		patch,
		gen.ThemePreference_THEME_PREFERENCE_DARK,
	))
	require.Error(t, usersettings.ValidateResetPaths(patch, []string{path}))
}

func TestResetValidationTreatsExplicitZeroValuesAsPatchConflicts(t *testing.T) {
	patch := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Regional.Locale.Set(patch, ""))
	require.NoError(t, usersettings.Fields.Email.Product.Set(patch, false))

	require.ErrorContains(t, usersettings.ValidateResetPaths(
		patch,
		[]string{usersettings.Fields.Regional.Locale.Path()},
	), "cannot be patched and reset")
	require.ErrorContains(t, usersettings.ValidateResetPaths(
		patch,
		[]string{usersettings.Fields.Email.Product.Path()},
	), "cannot be patched and reset")
}

func TestMultipleResetsPruneOnlyParentsWhoseLastChildWasCleared(t *testing.T) {
	settings := &gen.UserSettings{}
	require.NoError(t, usersettings.Fields.Regional.Locale.Set(settings, "fr"))
	require.NoError(t, usersettings.Fields.Regional.Timezone.Set(settings, "Europe/Paris"))
	require.NoError(t, usersettings.Fields.Email.Product.Set(settings, false))
	require.NoError(t, usersettings.Fields.Email.Marketing.Set(settings, true))

	require.NoError(t, usersettings.ApplyResets(settings, []string{
		usersettings.Fields.Regional.Locale.Path(),
		usersettings.Fields.Email.Product.Path(),
	}))
	require.NotNil(t, settings.Regional)
	require.Nil(t, settings.Regional.Locale)
	require.Equal(t, "Europe/Paris", settings.Regional.GetTimezone())
	require.NotNil(t, settings.Email)
	require.Nil(t, settings.Email.Product)
	require.True(t, settings.Email.GetMarketing())

	require.NoError(t, usersettings.ApplyResets(settings, []string{
		usersettings.Fields.Regional.Timezone.Path(),
		usersettings.Fields.Email.Marketing.Path(),
	}))
	require.Nil(t, settings.Regional)
	require.Nil(t, settings.Email)
}
