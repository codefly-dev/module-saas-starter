package adapters

import (
	"net/http"
	"strings"
	"time"

	"accounts/pkg/business"
)

// requireHTTPMFA is the HTTP-handler equivalent of the strict gRPC
// requireRecentMFA gate (see auth.go). Used by billing endpoints which run as plain
// http.Handlers rather than going through the Connect/gRPC mux — they
// don't get the auth interceptor's assurance context for free, so we
// re-parse the bearer here.
//
// Behaviour is deliberately strict for money-moving operations:
//   - Bearer token has recent AAL2 evidence → pass.
//   - Missing, stale, or AAL1 evidence → fail, even before enrollment.
//
// The error is returned (not written to w) so callers can choose the
// HTTP status code — billing returns 412 Precondition Failed to match
// the gRPC FailedPrecondition; other consumers may pick a different
// code.
func requireHTTPMFA(svc *business.Service, r *http.Request, _ string) error {
	// First, check the signed assurance claims. Refresh rotation preserves
	// MFAVerifiedAt, so it cannot silently extend the step-up window.
	authz := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(authz, "Bearer "); ok && token != "" {
		if minter := svc.JWTMinter(); minter != nil {
			if id, err := minter.VerifyAccess(token); err == nil && id.Assurance().HasRecentMFA(time.Now(), recentStepUpMaxAge) {
				return nil
			}
		}
	}

	return ErrMFARequired
}

// ErrMFARequired and ErrMFACheckFailed are sentinel errors HTTP
// callers can branch on. Their messages are stable strings the
// frontend can show or branch on.
var (
	ErrMFARequired = httpError{msg: "mfa_required"}
)

type httpError struct{ msg string }

func (e httpError) Error() string { return e.msg }
