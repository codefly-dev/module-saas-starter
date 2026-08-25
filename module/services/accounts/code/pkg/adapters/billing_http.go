package adapters

// HTTP handlers for the two user-facing billing endpoints:
//
//   POST /v1/billing/checkout  { plan_name }
//     → { url }        // Stripe-hosted checkout URL
//
//   POST /v1/billing/portal    {}
//     → { url }        // Stripe-hosted self-service billing portal
//
// Both are AUTHENTICATED. Forwarded identity is accepted only with the
// gateway credential; otherwise a signed access token is verified locally.
// Raw X-User-ID / X-Org-ID headers are never authority.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewBillingHTTPHandler returns an http.Handler that routes the two
// billing endpoints against the given service. Mount via
// RegisterHTTPRoute("/v1/billing/", handler) — the handler dispatches
// on the trailing path.
func NewBillingHTTPHandler(svc *business.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/checkout", func(w http.ResponseWriter, r *http.Request) {
		handleCheckout(svc, w, r)
	})
	mux.HandleFunc("/v1/billing/free-plan", func(w http.ResponseWriter, r *http.Request) {
		handleFreePlan(svc, w, r)
	})
	mux.HandleFunc("/v1/billing/portal", func(w http.ResponseWriter, r *http.Request) {
		handlePortal(svc, w, r)
	})
	return mux
}

type checkoutRequest struct {
	PlanName string `json:"plan_name"`
}

func handleCheckout(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	ctx, userID, orgID, err := authenticateBillingHTTPRequest(svc, r)
	if err != nil {
		writeBillingAuthnError(w, r, err)
		return
	}
	r = r.WithContext(ctx)
	if err := requireBillingAdmin(ctx, userID, orgID); err != nil {
		writeHTTPBillingAuthzError(w, err)
		return
	}
	if err := requireRecentMFA(ctx); err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeJSONError(w, http.StatusBadRequest, "Idempotency-Key header required")
		return
	}

	var req checkoutRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.PlanName == "" {
		writeJSONError(w, http.StatusBadRequest, "plan_name required")
		return
	}

	url, err := svc.StartCheckout(r.Context(), business.StartCheckoutInput{
		UserID:         userID,
		OrgID:          orgID,
		PlanName:       req.PlanName,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func handleFreePlan(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	ctx, userID, orgID, err := authenticateBillingHTTPRequest(svc, r)
	if err != nil {
		writeBillingAuthnError(w, r, err)
		return
	}
	r = r.WithContext(ctx)
	if err := requireBillingAdmin(ctx, userID, orgID); err != nil {
		writeHTTPBillingAuthzError(w, err)
		return
	}
	if err := requireRecentMFA(ctx); err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		writeJSONError(w, http.StatusBadRequest, "Idempotency-Key header required")
		return
	}
	if err := svc.SelectFreePlan(r.Context(), userID, orgID); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "active"})
}

func handlePortal(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	ctx, userID, orgID, err := authenticateBillingHTTPRequest(svc, r)
	if err != nil {
		writeBillingAuthnError(w, r, err)
		return
	}
	r = r.WithContext(ctx)
	if err := requireBillingAdmin(ctx, userID, orgID); err != nil {
		writeHTTPBillingAuthzError(w, err)
		return
	}
	if err := requireRecentMFA(ctx); err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeJSONError(w, http.StatusBadRequest, "Idempotency-Key header required")
		return
	}

	url, err := svc.OpenBillingPortal(r.Context(), business.OpenBillingPortalInput{
		UserID:         userID,
		OrgID:          orgID,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// authenticateBillingHTTPRequest establishes the exact same private identity
// used by Connect/gRPC before any authorization cache or database operation.
// The returned IDs are read back from that private context, never copied from
// untrusted transport headers.
func authenticateBillingHTTPRequest(svc *business.Service, r *http.Request) (context.Context, string, string, error) {
	if svc == nil || r == nil {
		return nil, "", "", errors.New("billing authentication is unavailable")
	}
	ctx := r.Context()
	if validGatewayToken(r.Header.Get("X-Codefly-Gateway-Token")) && r.Header.Get("X-User-Id") != "" {
		ctx = stampForwardedHTTPIdentity(ctx, r.Header)
	} else {
		minter := svc.JWTMinter()
		if minter == nil {
			return ctx, "", "", errors.New("access-token verifier is unavailable")
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			return ctx, "", "", errors.New("bearer token is required")
		}
		identity, err := minter.VerifyAccess(token)
		if err != nil {
			// Preserve the revocation-store-outage sentinel so the caller can
			// answer 503 (retryable) instead of collapsing it into a 401; every
			// other verify failure stays a generic invalid-credentials answer.
			if errors.Is(err, auth.ErrRevocationUnavailable) {
				return ctx, "", "", err
			}
			return ctx, "", "", errors.New("access token is invalid")
		}
		if identity == nil {
			return ctx, "", "", errors.New("access token is invalid")
		}
		ctx = stampVerifiedIdentity(ctx, identity.UserID.String(), identity.OrgID.String(), identity.Assurance())
	}
	tenantID, userID, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok {
		return ctx, "", "", auth.ErrVerifiedDatabaseIdentityRequired
	}
	return ctx, userID, tenantID, nil
}

// writeBillingAuthnError answers a billing authentication failure. A revocation
// list outage is a retryable operator-side condition, so it surfaces as 503 and
// is logged (parity with the RPC interceptors); every other failure collapses to
// the generic 401 so it can't be used as a credential oracle.
func writeBillingAuthnError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrRevocationUnavailable) {
		wool.Get(r.Context()).In("billingHTTP").Warn("revocation list unavailable, denying (fail-closed)", wool.ErrField(err))
		writeJSONError(w, http.StatusServiceUnavailable, "authorization temporarily unavailable")
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "authentication required")
}

func writeHTTPBillingAuthzError(w http.ResponseWriter, err error) {
	switch status.Code(err) {
	case codes.InvalidArgument:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case codes.Internal:
		writeJSONError(w, http.StatusInternalServerError, "billing authorization unavailable")
	default:
		writeJSONError(w, http.StatusForbidden, "billing administrator permission required")
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
