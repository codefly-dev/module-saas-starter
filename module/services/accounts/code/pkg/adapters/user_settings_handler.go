package adapters

import (
	"context"

	"connectrpc.com/connect"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// userSettingsConnectHandler exposes the per-user JSONB preferences
// blob. No org scoping — the auth interceptor's caller id determines
// whose settings are read/written. Users editing their own settings
// is the only flow; admin-overrides happen through PlatformAdmin.
type userSettingsConnectHandler struct{ svc *business.Service }

func (h *userSettingsConnectHandler) Get(
	ctx context.Context,
	req *connect.Request[gen.GetUserSettingsRequest],
) (*connect.Response[gen.UserSettings], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := h.svc.GetUserSettings(ctx, actorID)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(userSettingsToProto(settings)), nil
}

func (h *userSettingsConnectHandler) Update(
	ctx context.Context,
	req *connect.Request[gen.UpdateUserSettingsRequest],
) (*connect.Response[gen.UserSettings], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	patch := userSettingsFromProto(req.Msg.Patch)
	settings, err := h.svc.UpdateUserSettings(ctx, actorID, patch)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(userSettingsToProto(settings)), nil
}

// ─ converters ───────────────────────────────────────────────────

func userSettingsToProto(s *business.UserSettings) *gen.UserSettings {
	if s == nil {
		return &gen.UserSettings{}
	}
	out := &gen.UserSettings{
		Locale:     s.Locale,
		Timezone:   s.Timezone,
		DateFormat: s.DateFormat,
		TimeFormat: s.TimeFormat,
	}
	if s.Theme != nil {
		preference := themePreferenceToProto(*s.Theme)
		out.Theme = &preference
	}
	if s.Email != nil {
		out.Email = &gen.UserEmailSettings{
			Product:      s.Email.Product,
			Marketing:    s.Email.Marketing,
			Security:     s.Email.Security,
			WeeklyDigest: s.Email.WeeklyDigest,
		}
	}
	if s.Notifications != nil {
		out.Notifications = &gen.UserNotificationSettings{
			InApp: s.Notifications.InApp,
			Push:  s.Notifications.Push,
			Sound: s.Notifications.Sound,
		}
	}
	return out
}

func userSettingsFromProto(p *gen.UserSettings) *business.UserSettings {
	if p == nil {
		return &business.UserSettings{}
	}
	out := &business.UserSettings{
		Locale:     p.Locale,
		Timezone:   p.Timezone,
		DateFormat: p.DateFormat,
		TimeFormat: p.TimeFormat,
	}
	if p.Theme != nil {
		preference := themePreferenceFromProto(*p.Theme)
		out.Theme = &preference
	}
	if p.Email != nil {
		out.Email = &business.EmailSettings{
			Product:      p.Email.Product,
			Marketing:    p.Email.Marketing,
			Security:     p.Email.Security,
			WeeklyDigest: p.Email.WeeklyDigest,
		}
	}
	if p.Notifications != nil {
		out.Notifications = &business.NotificationSettings{
			InApp: p.Notifications.InApp,
			Push:  p.Notifications.Push,
			Sound: p.Notifications.Sound,
		}
	}
	return out
}

func themePreferenceToProto(preference business.ThemePreference) gen.ThemePreference {
	switch preference {
	case business.ThemePreferenceSystem:
		return gen.ThemePreference_THEME_PREFERENCE_SYSTEM
	case business.ThemePreferenceLight:
		return gen.ThemePreference_THEME_PREFERENCE_LIGHT
	case business.ThemePreferenceDark:
		return gen.ThemePreference_THEME_PREFERENCE_DARK
	default:
		return gen.ThemePreference_THEME_PREFERENCE_UNSPECIFIED
	}
}

func themePreferenceFromProto(preference gen.ThemePreference) business.ThemePreference {
	switch preference {
	case gen.ThemePreference_THEME_PREFERENCE_SYSTEM:
		return business.ThemePreferenceSystem
	case gen.ThemePreference_THEME_PREFERENCE_LIGHT:
		return business.ThemePreferenceLight
	case gen.ThemePreference_THEME_PREFERENCE_DARK:
		return business.ThemePreferenceDark
	default:
		return ""
	}
}
