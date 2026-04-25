package adapters

import (
	"net/http"
	"strings"

	"api/pkg/business"
)

// requireHTTPMFA is the HTTP-handler equivalent of the gRPC requireMFA
// gate (see auth.go). Used by billing endpoints which run as plain
// http.Handlers rather than going through the Connect/gRPC mux — they
// don't get the auth interceptor's mfaSatisfiedCtxKey for free, so we
// re-parse the bearer here.
//
// Behaviour mirrors requireMFA:
//   - Bearer token has mfa=true claim → pass (challenge already cleared
//     this session).
//   - User has no verified MFA device → pass (can't enforce; mirrors
//     opt-in-per-user policy).
//   - Otherwise → return non-nil error; caller surfaces as 412.
//
// The error is returned (not written to w) so callers can choose the
// HTTP status code — billing returns 412 Precondition Failed to match
// the gRPC FailedPrecondition; other consumers may pick a different
// code.
func requireHTTPMFA(svc *business.Service, r *http.Request, userID string) error {
	// First, check the bearer for the mfa=true claim. This is cheap —
	// no DB hit when the session already cleared the gate.
	authz := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(authz, "Bearer "); ok && token != "" {
		if minter := svc.JWTMinter(); minter != nil {
			if id, err := minter.VerifyAccess(token); err == nil && id.MFASatisfied {
				return nil
			}
		}
	}

	// Bearer doesn't carry mfa=true — fall back to the user-enrollment
	// check. No verified device → can't enforce → pass.
	enrolled, err := svc.Store().HasVerifiedMFA(r.Context(), userID)
	if err != nil {
		// Fail closed: better to block billing than to default-allow
		// when the lookup fails.
		return ErrMFACheckFailed
	}
	if !enrolled {
		return nil
	}
	return ErrMFARequired
}

// ErrMFARequired and ErrMFACheckFailed are sentinel errors HTTP
// callers can branch on. Their messages are stable strings the
// frontend can show or branch on.
var (
	ErrMFARequired    = httpError{msg: "mfa_required"}
	ErrMFACheckFailed = httpError{msg: "mfa_check_failed"}
)

type httpError struct{ msg string }

func (e httpError) Error() string { return e.msg }
