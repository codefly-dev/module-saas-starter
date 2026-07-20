package adapters

import (
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestUserSettingsThemePreferenceRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		business  business.ThemePreference
		generated gen.ThemePreference
	}{
		{"system", business.ThemePreferenceSystem, gen.ThemePreference_THEME_PREFERENCE_SYSTEM},
		{"light", business.ThemePreferenceLight, gen.ThemePreference_THEME_PREFERENCE_LIGHT},
		{"dark", business.ThemePreferenceDark, gen.ThemePreference_THEME_PREFERENCE_DARK},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded := userSettingsToProto(&business.UserSettings{Theme: &test.business})
			if encoded.Theme == nil || *encoded.Theme != test.generated {
				t.Fatalf("generated theme = %v, want %v", encoded.Theme, test.generated)
			}
			decoded := userSettingsFromProto(encoded)
			if decoded.Theme == nil || *decoded.Theme != test.business {
				t.Fatalf("business theme = %v, want %v", decoded.Theme, test.business)
			}
		})
	}
}

func TestUserSettingsUnspecifiedThemeFailsClosed(t *testing.T) {
	t.Parallel()
	preference := gen.ThemePreference_THEME_PREFERENCE_UNSPECIFIED
	decoded := userSettingsFromProto(&gen.UserSettings{Theme: &preference})
	if decoded.Theme == nil || *decoded.Theme != "" {
		t.Fatalf("unspecified theme = %v, want invalid empty preference", decoded.Theme)
	}
}
