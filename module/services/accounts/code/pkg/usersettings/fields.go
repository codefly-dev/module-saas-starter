// Package usersettings is the generated-protobuf access layer for Starter
// user preferences. Product code never addresses JSON keys directly.
package usersettings

import (
	"fmt"
	"strings"

	saassettings "accounts/pkg/settings"
	"accounts/pkg/settingscatalog"

	"google.golang.org/protobuf/proto"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

const MaximumJSONBytes = 128 * 1024

// Fields is the typed settings catalog. Every scalar in a settings proto must
// be optional so Lookup can distinguish an explicitly stored zero value from
// an unset value that should resolve to the configured default.
var Fields = struct {
	Appearance struct {
		Theme saassettings.Field[*gen.UserSettings, gen.ThemePreference]
	}
	Regional struct {
		Locale     saassettings.Field[*gen.UserSettings, string]
		Timezone   saassettings.Field[*gen.UserSettings, string]
		DateFormat saassettings.Field[*gen.UserSettings, string]
		TimeFormat saassettings.Field[*gen.UserSettings, string]
	}
	Email struct {
		Product      saassettings.Field[*gen.UserSettings, bool]
		Marketing    saassettings.Field[*gen.UserSettings, bool]
		Security     saassettings.Field[*gen.UserSettings, bool]
		WeeklyDigest saassettings.Field[*gen.UserSettings, bool]
	}
	Notifications struct {
		InApp saassettings.Field[*gen.UserSettings, bool]
		Push  saassettings.Field[*gen.UserSettings, bool]
		Sound saassettings.Field[*gen.UserSettings, bool]
	}
}{
	Appearance: struct {
		Theme saassettings.Field[*gen.UserSettings, gen.ThemePreference]
	}{
		Theme: saassettings.MustEnum(
			&gen.UserSettings{},
			"appearance.theme",
			gen.ThemePreference_THEME_PREFERENCE_SYSTEM,
		),
	},
	Regional: struct {
		Locale     saassettings.Field[*gen.UserSettings, string]
		Timezone   saassettings.Field[*gen.UserSettings, string]
		DateFormat saassettings.Field[*gen.UserSettings, string]
		TimeFormat saassettings.Field[*gen.UserSettings, string]
	}{
		Locale: saassettings.MustString(
			&gen.UserSettings{},
			"regional.locale",
			"en",
		),
		Timezone: saassettings.MustString(
			&gen.UserSettings{},
			"regional.timezone",
			"UTC",
		),
		DateFormat: saassettings.MustString(
			&gen.UserSettings{},
			"regional.date_format",
			"iso",
		),
		TimeFormat: saassettings.MustString(
			&gen.UserSettings{},
			"regional.time_format",
			"24h",
		),
	},
	Email: struct {
		Product      saassettings.Field[*gen.UserSettings, bool]
		Marketing    saassettings.Field[*gen.UserSettings, bool]
		Security     saassettings.Field[*gen.UserSettings, bool]
		WeeklyDigest saassettings.Field[*gen.UserSettings, bool]
	}{
		Product: saassettings.MustBool(
			&gen.UserSettings{},
			"email.product",
			true,
		),
		Marketing: saassettings.MustBool(
			&gen.UserSettings{},
			"email.marketing",
			false,
		),
		Security: saassettings.MustBool(
			&gen.UserSettings{},
			"email.security",
			true,
		),
		WeeklyDigest: saassettings.MustBool(
			&gen.UserSettings{},
			"email.weekly_digest",
			true,
		),
	},
	Notifications: struct {
		InApp saassettings.Field[*gen.UserSettings, bool]
		Push  saassettings.Field[*gen.UserSettings, bool]
		Sound saassettings.Field[*gen.UserSettings, bool]
	}{
		InApp: saassettings.MustBool(
			&gen.UserSettings{},
			"notifications.in_app",
			true,
		),
		Push: saassettings.MustBool(
			&gen.UserSettings{},
			"notifications.push",
			false,
		),
		Sound: saassettings.MustBool(
			&gen.UserSettings{},
			"notifications.sound",
			false,
		),
	},
}

var JSON = saassettings.MustJSONCodec(
	func() *gen.UserSettings { return &gen.UserSettings{} },
	MaximumJSONBytes,
)

type resetField struct {
	clear func(*gen.UserSettings) error
	has   func(*gen.UserSettings) (bool, error)
}

func resettable[T any](
	field saassettings.Field[*gen.UserSettings, T],
) resetField {
	return resetField{clear: field.Clear, has: field.Has}
}

// resetFields is the complete API allowlist for clearing an explicit override
// back to its catalog default. Product code obtains strings from typed fields;
// callers cannot delete arbitrary ProtoJSON paths.
var resetFields = map[string]resetField{
	Fields.Appearance.Theme.Path():    resettable(Fields.Appearance.Theme),
	Fields.Regional.Locale.Path():     resettable(Fields.Regional.Locale),
	Fields.Regional.Timezone.Path():   resettable(Fields.Regional.Timezone),
	Fields.Regional.DateFormat.Path(): resettable(Fields.Regional.DateFormat),
	Fields.Regional.TimeFormat.Path(): resettable(Fields.Regional.TimeFormat),
	Fields.Email.Product.Path():       resettable(Fields.Email.Product),
	Fields.Email.Marketing.Path():     resettable(Fields.Email.Marketing),
	Fields.Email.Security.Path():      resettable(Fields.Email.Security),
	Fields.Email.WeeklyDigest.Path():  resettable(Fields.Email.WeeklyDigest),
	Fields.Notifications.InApp.Path(): resettable(Fields.Notifications.InApp),
	Fields.Notifications.Push.Path():  resettable(Fields.Notifications.Push),
	Fields.Notifications.Sound.Path(): resettable(Fields.Notifications.Sound),
}

func resetFieldForPath(path string) (resetField, bool, error) {
	if field, ok := resetFields[path]; ok {
		return field, true, nil
	}
	name, found := strings.CutPrefix(path, "composed.")
	if !found || name == "" || strings.Contains(name, ".") {
		return resetField{}, false, nil
	}
	for _, field := range settingscatalog.Fields() {
		if field.Name != name {
			continue
		}
		if err := saassettings.ValidateComposedField(&gen.UserSettings{}, "composed", name); err != nil {
			return resetField{}, false, err
		}
		return resetField{
			clear: func(settings *gen.UserSettings) error {
				_, err := saassettings.AccessComposedField(settings, "composed", name, true)
				return err
			},
			has: func(settings *gen.UserSettings) (bool, error) {
				return saassettings.AccessComposedField(settings, "composed", name, false)
			},
		}, true, nil
	}
	return resetField{}, false, nil
}

// ValidateResetPaths rejects unknown paths, duplicates, and ambiguous requests
// that both patch and reset the same field.
func ValidateResetPaths(patch *gen.UserSettings, paths []string) error {
	if patch == nil {
		patch = &gen.UserSettings{}
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		field, ok, err := resetFieldForPath(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("settings reset path %q is not supported", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("settings reset path %q is duplicated", path)
		}
		seen[path] = struct{}{}
		present, err := field.has(patch)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("settings path %q cannot be patched and reset together", path)
		}
	}
	return nil
}

// ApplyResets clears typed fields from an in-memory document. Persistence
// adapters apply the same validated paths directly to sparse ProtoJSON.
func ApplyResets(settings *gen.UserSettings, paths []string) error {
	for _, path := range paths {
		field, ok, err := resetFieldForPath(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("settings reset path %q is not supported", path)
		}
		if err := field.clear(settings); err != nil {
			return err
		}
	}
	return nil
}

// Resolve returns a clone with every common Starter default materialized.
// Stored documents stay sparse; API consumers receive a complete typed model.
func Resolve(stored *gen.UserSettings) (*gen.UserSettings, error) {
	resolved := &gen.UserSettings{}
	if stored != nil {
		resolved = proto.Clone(stored).(*gen.UserSettings)
	}
	if err := applyDefault(Fields.Appearance.Theme, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Regional.Locale, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Regional.Timezone, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Regional.DateFormat, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Regional.TimeFormat, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Email.Product, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Email.Marketing, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Email.Security, resolved); err != nil {
		return nil, err
	}
	if err := Fields.Email.Security.Set(resolved, true); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Email.WeeklyDigest, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Notifications.InApp, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Notifications.Push, resolved); err != nil {
		return nil, err
	}
	if err := applyDefault(Fields.Notifications.Sound, resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func applyDefault[T any](
	field saassettings.Field[*gen.UserSettings, T],
	settings *gen.UserSettings,
) error {
	_, err := field.ApplyDefault(settings)
	return err
}
