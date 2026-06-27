package adapters

// HTTP handlers for the two user-facing billing endpoints:
//
//   POST /v1/billing/checkout  { plan_name, success_url, cancel_url, trial_days? }
//     → { url }        // Stripe-hosted checkout URL
//
//   POST /v1/billing/portal    { return_url }
//     → { url }        // Stripe-hosted self-service billing portal
//
// Both are AUTHENTICATED — the sidecar forwards X-User-ID and
// X-Org-ID, and the handlers trust those headers as the caller's
// identity.

import (
	"encoding/json"
	"io"
	"net/http"

	"accounts/pkg/business"
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
	mux.HandleFunc("/v1/billing/portal", func(w http.ResponseWriter, r *http.Request) {
		handlePortal(svc, w, r)
	})
	return mux
}

type checkoutRequest struct {
	PlanName   string `json:"plan_name"`
	SuccessURL string `json:"success_url"`
	CancelURL  string `json:"cancel_url"`
	TrialDays  int    `json:"trial_days,omitempty"`
}

func handleCheckout(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	userID := r.Header.Get("X-User-ID")
	orgID := r.Header.Get("X-Org-ID")
	if userID == "" || orgID == "" {
		writeJSONError(w, http.StatusUnauthorized, "missing X-User-ID or X-Org-ID")
		return
	}
	if err := requireHTTPMFA(svc, r, userID); err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
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
	if req.PlanName == "" || req.SuccessURL == "" || req.CancelURL == "" {
		writeJSONError(w, http.StatusBadRequest, "plan_name, success_url, cancel_url required")
		return
	}

	url, err := svc.StartCheckout(r.Context(), business.StartCheckoutInput{
		UserID:     userID,
		OrgID:      orgID,
		PlanName:   req.PlanName,
		SuccessURL: req.SuccessURL,
		CancelURL:  req.CancelURL,
		TrialDays:  req.TrialDays,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

type portalRequest struct {
	ReturnURL string `json:"return_url"`
}

func handlePortal(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	userID := r.Header.Get("X-User-ID")
	orgID := r.Header.Get("X-Org-ID")
	if userID == "" || orgID == "" {
		writeJSONError(w, http.StatusUnauthorized, "missing X-User-ID or X-Org-ID")
		return
	}
	if err := requireHTTPMFA(svc, r, userID); err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	var req portalRequest
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	_ = json.Unmarshal(body, &req)
	if req.ReturnURL == "" {
		writeJSONError(w, http.StatusBadRequest, "return_url required")
		return
	}

	url, err := svc.OpenBillingPortal(r.Context(), business.OpenBillingPortalInput{
		UserID:    userID,
		OrgID:     orgID,
		ReturnURL: req.ReturnURL,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
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
