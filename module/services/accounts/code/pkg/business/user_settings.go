package business

import (
	"context"
	"encoding/json"
	"fmt"
)

// UserSettings — the canonical shape of the per-user preferences blob
// stored as JSONB on the users row. Adding a field here is a no-op
// schema-wise (just bump the proto + FE), removing one is too
// (existing rows just keep the orphan key in their JSON until next
// write). That's the whole point of JSONB over typed columns.
//
// All fields are optional / pointer-typed because the FE submits
// partials — only the keys it changed — and the api jsonb_merge's
// them onto the stored value. A nil pointer means "no opinion, leave
// stored value alone".
type UserSettings struct {
	// "light" | "dark" | "system" — synced with next-themes on the FE.
	Theme *string `json:"theme,omitempty"`
	// IETF language tag: "en", "en-US", "fr", "es-419", etc.
	Locale *string `json:"locale,omitempty"`
	// IANA tz: "America/New_York", "Europe/Paris", "UTC".
	Timezone *string `json:"timezone,omitempty"`
	// "iso" (2026-04-25), "us" (04/25/2026), "eu" (25/04/2026).
	DateFormat *string `json:"date_format,omitempty"`
	// "12h" (3:45 PM) | "24h" (15:45).
	TimeFormat *string `json:"time_format,omitempty"`

	// Top-level email opt-ins. Per-event-type granularity lives in the
	// notification_preferences flow — these toggle the macro categories
	// the unsubscribe link in transactional emails respects.
	Email *EmailSettings `json:"email,omitempty"`

	// Notifications — global on/off for the in-app + push channels.
	Notifications *NotificationSettings `json:"notifications,omitempty"`
}

// EmailSettings — top-level transactional email opt-ins. Security is
// not exposed as a toggle (we always send security alerts) but is
// tracked here so the unsubscribe page can show its forced-on state.
type EmailSettings struct {
	Product       *bool `json:"product,omitempty"`        // product updates
	Marketing     *bool `json:"marketing,omitempty"`      // newsletters / promos
	Security      *bool `json:"security,omitempty"`       // alerts (forced on; surfaced for transparency)
	WeeklyDigest  *bool `json:"weekly_digest,omitempty"`  // weekly activity rollup
}

type NotificationSettings struct {
	InApp *bool `json:"in_app,omitempty"`
	Push  *bool `json:"push,omitempty"`
	Sound *bool `json:"sound,omitempty"`
}

// GetUserSettings returns the user's settings blob. Empty users see
// an empty struct; the FE's defaults fill in missing fields client-
// side. Never errors on missing — only on store / json failures.
func (s *Service) GetUserSettings(ctx context.Context, userID string) (*UserSettings, error) {
	raw, err := s.store.GetUserSettings(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "{}" {
		return &UserSettings{}, nil
	}
	var out UserSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode settings: %w", err)
	}
	return &out, nil
}

// UpdateUserSettings merges a partial settings object onto the
// stored JSONB. The FE submits ONLY the keys it wants to change —
// nil pointers in `patch` mean "leave that key alone". This is the
// concatenation-merge that avoids the lost-update problem of
// "fetch / mutate / write the whole thing back".
//
// Implementation detail: the merge happens in postgres via the `||`
// operator (jsonb_merge wouldn't recurse the way we want anyway).
// Top-level keys present in `patch` overwrite their stored values
// entirely — nested objects (Email, Notifications) are replaced, not
// recursively merged. The FE accommodates by always sending the
// full nested object when it changes any key inside.
func (s *Service) UpdateUserSettings(ctx context.Context, userID string, patch *UserSettings) (*UserSettings, error) {
	if patch == nil {
		return s.GetUserSettings(ctx, userID)
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("encode patch: %w", err)
	}
	// Settings live on the RLS-protected users row; scope the tx to the user so
	// app.current_user_id is set (else the UPDATE silently matches zero rows and
	// settings never persist — same class of bug as consent).
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		return s.store.UpdateUserSettings(ctx, userID, body)
	}); err != nil {
		return nil, err
	}
	s.emit(ctx, userID, "user", "settings.updated", "user", userID, "")
	return s.GetUserSettings(ctx, userID)
}
