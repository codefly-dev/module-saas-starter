package adapters

import (
	"context"

	"connectrpc.com/connect"

	"api/pkg/business"
	"api/pkg/gen"
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
		Theme:      s.Theme,
		Locale:     s.Locale,
		Timezone:   s.Timezone,
		DateFormat: s.DateFormat,
		TimeFormat: s.TimeFormat,
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
		Theme:      p.Theme,
		Locale:     p.Locale,
		Timezone:   p.Timezone,
		DateFormat: p.DateFormat,
		TimeFormat: p.TimeFormat,
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
