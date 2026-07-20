package business

import (
	"context"
	"time"
)

// CurrentTermsVersion is the version string the api considers
// authoritative. Bumped by hand when the operator publishes a new
// TOS / privacy policy. Frontend reads this through GetConsentStatus
// to decide whether to show the ConsentBanner.
//
// Format is intentionally a date — easy to grok at a glance, sorts
// monotonically, doesn't collide. If you want semver, swap in
// "v1" / "v2" — the comparison is byte-equality, not <.
const CurrentTermsVersion = "2026-04-25"

// UserConsentStatus is the resolved view of where a user stands
// against the current terms. Empty AcceptedVersion = never accepted.
type UserConsentStatus struct {
	AcceptedVersion string
	AcceptedAt      *time.Time
	CurrentVersion  string
}

// GetConsentStatus returns the user's last-accepted version + the
// current authoritative version. The FE compares; if they differ
// (including AcceptedVersion empty), it shows the banner.
func (s *Service) GetConsentStatus(ctx context.Context, userID string) (*UserConsentStatus, error) {
	var v string
	var at *time.Time
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var err error
		v, at, err = s.store.GetUserConsent(ctx, userID)
		return err
	}); err != nil {
		return nil, err
	}
	return &UserConsentStatus{
		AcceptedVersion: v,
		AcceptedAt:      at,
		CurrentVersion:  CurrentTermsVersion,
	}, nil
}

// AcceptConsent records that userID accepted version. Idempotent —
// re-accepting the same (user, version) pair just refreshes the
// timestamp, which is fine. The audit-log entry is the
// immutable acceptance record (see emit below).
//
// We deliberately accept ANY version string the FE submits rather
// than requiring it == CurrentTermsVersion. Reason: a long-running
// browser session may still have an older banner cached when the
// operator bumps CurrentTermsVersion mid-deploy; recording the
// version the user actually saw is more honest than refusing the
// click. Next page load they'll see the new version and accept it.
func (s *Service) AcceptConsent(ctx context.Context, userID, version string) error {
	// The write targets the caller's own users row, which is RLS-protected
	// (users_update: uuid == app.current_user_id). Scope the tx to the user so
	// the GUC is set — without this the UPDATE matches zero rows under the
	// app_tenant role and consent silently never persists (banner reappears).
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		return s.store.SetUserConsent(ctx, userID, version, time.Now())
	}); err != nil {
		return err
	}
	s.emit(ctx, userID, "user", "consent.accepted", "user", userID, "")
	return nil
}
