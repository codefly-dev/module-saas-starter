package infra_test

import (
	"context"
	"sync"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"

	"github.com/stretchr/testify/require"
)

func TestUserSettingsProtoJSONDeepMergePreservesNestedSiblings(t *testing.T) {
	userID := seedUser(t)
	scope := testStore.As(business.Identity{UserID: userID})

	require.NoError(t, scope.Within(testCtx, func(ctx context.Context) error {
		first := &gen.UserSettings{}
		require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
			first,
			gen.ThemePreference_THEME_PREFERENCE_SYSTEM,
		))
		require.NoError(t, usersettings.Fields.Email.Product.Set(first, true))
		require.NoError(t, usersettings.Fields.Email.Marketing.Set(first, true))
		if err := testStore.UpdateUserSettings(ctx, userID, first, nil); err != nil {
			return err
		}

		patch := &gen.UserSettings{}
		require.NoError(t, usersettings.Fields.Regional.Locale.Set(patch, "fr"))
		require.NoError(t, usersettings.Fields.Email.Product.Set(patch, false))
		if err := testStore.UpdateUserSettings(ctx, userID, patch, nil); err != nil {
			return err
		}

		stored, err := testStore.GetUserSettings(ctx, userID)
		if err != nil {
			return err
		}
		require.Equal(t, gen.ThemePreference_THEME_PREFERENCE_SYSTEM, stored.Appearance.GetTheme())
		require.Equal(t, "fr", stored.Regional.GetLocale())
		require.NotNil(t, stored.Email.Product)
		require.False(t, stored.Email.GetProduct())
		require.NotNil(t, stored.Email.Marketing)
		require.True(t, stored.Email.GetMarketing(), "nested sibling must survive")
		return nil
	}))
}

func TestUserSettingsResetPrunesEmptyParentsAndPreservesSiblings(t *testing.T) {
	userID := seedUser(t)
	scope := testStore.As(business.Identity{UserID: userID})

	require.NoError(t, scope.Within(testCtx, func(ctx context.Context) error {
		seed := &gen.UserSettings{}
		require.NoError(t, usersettings.Fields.Appearance.Theme.Set(
			seed,
			gen.ThemePreference_THEME_PREFERENCE_DARK,
		))
		require.NoError(t, usersettings.Fields.Email.Product.Set(seed, false))
		require.NoError(t, usersettings.Fields.Email.Marketing.Set(seed, true))
		if err := testStore.UpdateUserSettings(ctx, userID, seed, nil); err != nil {
			return err
		}

		if err := testStore.UpdateUserSettings(
			ctx,
			userID,
			nil,
			[]string{
				usersettings.Fields.Appearance.Theme.Path(),
				usersettings.Fields.Email.Product.Path(),
			},
		); err != nil {
			return err
		}

		stored, err := testStore.GetUserSettings(ctx, userID)
		if err != nil {
			return err
		}
		require.Nil(t, stored.Appearance, "last cleared child must prune its parent")
		require.Nil(t, stored.Email.Product)
		require.NotNil(t, stored.Email.Marketing)
		require.True(t, stored.Email.GetMarketing(), "uncleared sibling must survive")
		return nil
	}))
}

func TestSettingsJSONBFunctionsPreserveUnknownFieldsAndExplicitFalse(t *testing.T) {
	var encoded []byte
	err := testPool.QueryRow(testCtx, `
		SELECT public.settings_jsonb_deep_merge(
			'{"future":{"new_field":true},"email":{"marketing":true}}'::jsonb,
			'{"email":{"product":false}}'::jsonb
		)
	`).Scan(&encoded)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"future": {"new_field": true},
		"email": {"marketing": true, "product": false}
	}`, string(encoded))

	err = testPool.QueryRow(testCtx, `
		SELECT public.settings_jsonb_delete_paths(
			'{"appearance":{"theme":"THEME_PREFERENCE_DARK"},"email":{"marketing":true,"product":false}}'::jsonb,
			ARRAY['appearance.theme','email.product']::text[]
		)
	`).Scan(&encoded)
	require.NoError(t, err)
	require.JSONEq(t, `{"email":{"marketing":true}}`, string(encoded))
}

func TestConcurrentSparseNestedPatchesDoNotLoseSiblings(t *testing.T) {
	userID := seedUser(t)
	scope := testStore.As(business.Identity{UserID: userID})

	patches := make([]*gen.UserSettings, 0, 8)
	add := func(set func(*gen.UserSettings) error) {
		patch := &gen.UserSettings{}
		require.NoError(t, set(patch))
		patches = append(patches, patch)
	}
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Regional.Locale.Set(patch, "")
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Regional.Timezone.Set(patch, "Europe/Paris")
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Regional.DateFormat.Set(patch, "eu")
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Email.Product.Set(patch, false)
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Email.Marketing.Set(patch, true)
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Email.Security.Set(patch, false)
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Notifications.InApp.Set(patch, false)
	})
	add(func(patch *gen.UserSettings) error {
		return usersettings.Fields.Notifications.Push.Set(patch, true)
	})

	start := make(chan struct{})
	errors := make(chan error, len(patches))
	var wait sync.WaitGroup
	for _, patch := range patches {
		wait.Add(1)
		go func(patch *gen.UserSettings) {
			defer wait.Done()
			<-start
			errors <- scope.Within(testCtx, func(ctx context.Context) error {
				return testStore.UpdateUserSettings(ctx, userID, patch, nil)
			})
		}(patch)
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	var stored *gen.UserSettings
	require.NoError(t, scope.Within(testCtx, func(ctx context.Context) error {
		var err error
		stored, err = testStore.GetUserSettings(ctx, userID)
		return err
	}))
	require.NotNil(t, stored.Regional.Locale)
	require.Empty(t, stored.Regional.GetLocale())
	require.Equal(t, "Europe/Paris", stored.Regional.GetTimezone())
	require.Equal(t, "eu", stored.Regional.GetDateFormat())
	require.NotNil(t, stored.Email.Product)
	require.False(t, stored.Email.GetProduct())
	require.True(t, stored.Email.GetMarketing())
	require.NotNil(t, stored.Email.Security)
	require.False(t, stored.Email.GetSecurity())
	require.NotNil(t, stored.Notifications.InApp)
	require.False(t, stored.Notifications.GetInApp())
	require.True(t, stored.Notifications.GetPush())
}
