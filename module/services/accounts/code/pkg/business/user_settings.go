package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetUserSettings returns the caller's generated protobuf settings document.
// JSON is confined to the Postgres adapter; business and transport code share
// this one canonical type.
func (s *Service) GetUserSettings(ctx context.Context, userID string) (*gen.UserSettings, error) {
	var settings *gen.UserSettings
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var err error
		settings, err = s.store.GetUserSettings(ctx, userID)
		return err
	}); err != nil {
		return nil, err
	}
	resolved, err := usersettings.Resolve(settings)
	if err != nil {
		return nil, status.Error(codes.Internal, "resolve user settings")
	}
	return resolved, nil
}

// UpdateUserSettings deep-merges a typed partial protobuf document. Optional
// scalar presence makes explicit false/empty values different from "leave this
// setting unchanged"; nested message merge is performed atomically by the
// Postgres adapter while holding the user row lock.
func (s *Service) UpdateUserSettings(
	ctx context.Context,
	userID string,
	patch *gen.UserSettings,
	resetPaths []string,
) (*gen.UserSettings, error) {
	if patch == nil && len(resetPaths) == 0 {
		return s.GetUserSettings(ctx, userID)
	}
	if patch == nil {
		patch = &gen.UserSettings{}
	}
	if err := usersettings.ValidateResetPaths(patch, resetPaths); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	theme, present, err := usersettings.Fields.Appearance.Theme.Lookup(patch)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid appearance theme")
	}
	if present {
		switch theme {
		case gen.ThemePreference_THEME_PREFERENCE_SYSTEM,
			gen.ThemePreference_THEME_PREFERENCE_LIGHT,
			gen.ThemePreference_THEME_PREFERENCE_DARK:
		default:
			return nil, status.Error(codes.InvalidArgument, "theme preference is invalid")
		}
	}

	var settings *gen.UserSettings
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		if err := s.store.UpdateUserSettings(ctx, userID, patch, resetPaths); err != nil {
			return err
		}
		var err error
		settings, err = s.store.GetUserSettings(ctx, userID)
		return err
	}); err != nil {
		return nil, err
	}
	s.emit(ctx, userID, "user", "settings.updated", "user", userID, "")
	resolved, err := usersettings.Resolve(settings)
	if err != nil {
		return nil, status.Error(codes.Internal, "resolve user settings")
	}
	return resolved, nil
}
