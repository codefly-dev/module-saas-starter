package auth

import (
	"context"
	"slices"
	"time"
)

const (
	// AssuranceLevelAAL1 represents a session established with one primary
	// authentication method. AssuranceLevelAAL2 requires a separately
	// verified second factor during the same authentication ceremony.
	AssuranceLevelAAL1 = "aal1"
	AssuranceLevelAAL2 = "aal2"

	AuthenticationMethodFixture  = "fixture"
	AuthenticationMethodOAuth    = "oauth"
	AuthenticationMethodEmail    = "email"
	AuthenticationMethodOTP      = "otp"
	AuthenticationMethodRecovery = "recovery"
	AuthenticationMethodWebAuthn = "webauthn"

	// DefaultRecentStepUpMaxAge is the maximum age accepted by sensitive
	// operations. Refresh rotation preserves the original verification time;
	// it never turns token refresh into a fresh authentication event.
	DefaultRecentStepUpMaxAge = 15 * time.Minute
)

// Assurance is the signed and persisted authentication evidence associated
// with a session. AuthenticatedAt records primary authentication; the
// separate MFAVerifiedAt timestamp supports an actual recent-step-up policy.
type Assurance struct {
	AuthenticationMethods []string
	AuthenticatedAt       time.Time
	Level                 string
	MFAVerifiedAt         time.Time
}

func (a Assurance) HasMethod(method string) bool {
	return slices.Contains(a.AuthenticationMethods, method)
}

// HasMFAEvidence reports whether the session contains durable evidence of a
// completed second-factor ceremony. Unlike HasRecentMFA, it does not impose a
// step-up age: refresh may preserve existing evidence, but never renew it.
func (a Assurance) HasMFAEvidence() bool {
	if a.Level != AssuranceLevelAAL2 || a.MFAVerifiedAt.IsZero() {
		return false
	}
	return a.HasMethod(AuthenticationMethodOTP) ||
		a.HasMethod(AuthenticationMethodRecovery) ||
		a.HasMethod(AuthenticationMethodWebAuthn)
}

// HasRecentMFA returns true only for an AAL2 session with explicit MFA
// evidence inside maxAge. A small future-skew allowance accommodates clocks,
// but a materially future-dated claim is rejected instead of becoming fresh
// indefinitely.
func (a Assurance) HasRecentMFA(now time.Time, maxAge time.Duration) bool {
	if !a.HasMFAEvidence() || maxAge <= 0 {
		return false
	}
	if a.MFAVerifiedAt.After(now.Add(time.Minute)) {
		return false
	}
	return !a.MFAVerifiedAt.Before(now.Add(-maxAge))
}

func (i *Identity) Assurance() Assurance {
	if i == nil {
		return Assurance{}
	}
	return Assurance{
		AuthenticationMethods: slices.Clone(i.AuthenticationMethods),
		AuthenticatedAt:       i.AuthenticatedAt,
		Level:                 i.AssuranceLevel,
		MFAVerifiedAt:         i.MFAVerifiedAt,
	}
}

type assuranceContextKey struct{}

// WithAssurance attaches evidence only after a signed JWT or trusted gateway
// projection has been verified. Callers must never populate it from raw,
// untrusted request headers.
func WithAssurance(ctx context.Context, assurance Assurance) context.Context {
	assurance.AuthenticationMethods = slices.Clone(assurance.AuthenticationMethods)
	return context.WithValue(ctx, assuranceContextKey{}, assurance)
}

func AssuranceFromContext(ctx context.Context) Assurance {
	assurance, _ := ctx.Value(assuranceContextKey{}).(Assurance)
	assurance.AuthenticationMethods = slices.Clone(assurance.AuthenticationMethods)
	return assurance
}
