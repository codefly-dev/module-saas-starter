package auth

import (
	"fmt"
	"strings"
)

// SignupMode gates whether a first-seen identity may provision a new account.
// It is consulted only by SignupIntent; LoginIntent and InviteIntent are never
// gated, so an invitee can always obtain an account while a stranger cannot.
type SignupMode int

const (
	// SignupModeOpen provisions any authenticated first-seen identity. This is
	// the historical behaviour and the default, so upgrading changes nothing.
	SignupModeOpen SignupMode = iota

	// SignupModeInvite provisions a first-seen identity only when a pending,
	// unexpired invitation matches its verified email, binding it to the
	// inviting organization.
	SignupModeInvite

	// SignupModeWaitlist provisions a first-seen identity only when an approved
	// or invited waitlist entry matches its verified email.
	SignupModeWaitlist
)

// ParseSignupMode maps a workspace configuration value to a SignupMode. An empty
// value selects the open default so an existing deployment is unaffected by the
// upgrade.
//
// Any other unrecognised value fails closed, exactly as OAuthRequestPolicy and
// the CORS policy do: it returns an error so startup refuses rather than
// silently widening access, and the mode it returns is the most restrictive so
// that a caller which ignores the error still rejects strangers.
func ParseSignupMode(raw string) (SignupMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "open":
		return SignupModeOpen, nil
	case "invite":
		return SignupModeInvite, nil
	case "waitlist":
		return SignupModeWaitlist, nil
	default:
		return SignupModeWaitlist, fmt.Errorf("auth: unrecognised signup mode %q (want open, invite, or waitlist)", raw)
	}
}
