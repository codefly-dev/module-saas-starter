package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// The delivery endpoint: `POST /platform/_delivery/{presence,authority}`.
//
// Mounted on ACCOUNTS rather than brokered through the gateway, and the
// asymmetry with the credential mint is deliberate. `POST
// /platform/_credential` is brokered because a solution runtime is an
// independently deployed workload that must not reach accounts' internal
// listener. A delivery Job is not that: it is in-cluster and one of two known
// service accounts, so routing it through the edge would put accounts-audience
// tokens across the perimeter for no gain.
//
// `/platform/` is RESERVED for the host, and an unknown `/platform/*` path
// answers 404 rather than falling through. That is a contract rather than
// tidiness: a consumer has to be able to tell "no such endpoint" from "endpoint
// broken", and a fall-through to generic routing answers 502 for the first.

// SolutionDeliveryPrefix is where the delivery endpoints are mounted. Published
// and pinned by the doc-claim gate, so a rename that does not move the document
// breaks every consumer silently.
const SolutionDeliveryPrefix = "/platform/_delivery/"

// solutionDeliveryMaxBytes bounds a carrier body.
//
// A presence document is a few kilobytes and a Sigstore bundle is tens; 1 MiB is
// far above both and far below anything that would cost the host to read. The
// bound exists because this endpoint authenticates the caller only AFTER parsing
// enough to know what was sent, so an unbounded read would be work an
// unauthenticated caller could ask for.
const solutionDeliveryMaxBytes = 1 << 20

type solutionDeliveryService interface {
	ReceiveSolutionDelivery(
		ctx context.Context, carrier business.SolutionDeliveryCarrier, credential string,
	) (business.SolutionDeliveryDisposition, *business.SolutionDeliveryRecord, error)
}

// SolutionDeliveryHTTPHandler serves the two delivery paths.
type SolutionDeliveryHTTPHandler struct {
	service solutionDeliveryService
}

// NewSolutionDeliveryHTTPHandler builds the handler.
func NewSolutionDeliveryHTTPHandler(service *business.Service) http.Handler {
	return &SolutionDeliveryHTTPHandler{service: service}
}

// solutionDeliveryReceipt is what a caller gets back. The disposition is echoed
// so a Job can log WHY it got the code it did — 200 and 202 are both success and
// mean different things, and a retry policy written against the code alone
// cannot tell a replay from a new generation.
type solutionDeliveryReceipt struct {
	Disposition string `json:"disposition"`
	DocumentID  string `json:"documentId,omitempty"`
	Generation  uint64 `json:"generation,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Signer      string `json:"signer,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (h *SolutionDeliveryHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// An unknown path under the reserved namespace is 404, never a fall-through.
	kind := business.SolutionDeliveryKind(strings.TrimPrefix(r.URL.Path, SolutionDeliveryPrefix))
	if !strings.HasPrefix(r.URL.Path, SolutionDeliveryPrefix) || !kind.Valid() {
		writeSolutionDeliveryError(w, http.StatusNotFound, "no such delivery endpoint")
		return
	}
	if r.Method != http.MethodPost {
		// 405 with Allow, so a GET probing the surface learns the method rather
		// than reading a 404 as "this host does not have delivery".
		w.Header().Set("Allow", http.MethodPost)
		writeSolutionDeliveryError(w, http.StatusMethodNotAllowed, "delivery is a POST")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, solutionDeliveryMaxBytes))
	if err != nil {
		writeSolutionDeliveryError(w, http.StatusBadRequest, "carrier body could not be read or exceeds the bound")
		return
	}

	disposition, record, err := h.service.ReceiveSolutionDelivery(
		r.Context(),
		business.SolutionDeliveryCarrier{Kind: kind, Raw: body},
		r.Header.Get("Authorization"),
	)
	if err != nil {
		status, message := solutionDeliveryStatus(err)
		writeSolutionDeliveryError(w, status, message)
		return
	}

	// 202 for a new generation now durable, 200 for an exact replay. Both are
	// success; the difference is whether anything changed, which is what a
	// pipeline's own idempotency depends on knowing.
	status := http.StatusAccepted
	if disposition == business.SolutionDeliveryReplayed {
		status = http.StatusOK
	}
	writeSolutionDeliveryJSON(w, status, solutionDeliveryReceipt{
		Disposition: string(disposition),
		DocumentID:  record.DocumentID,
		Generation:  record.Generation,
		ContentHash: record.ContentHash,
		Signer:      record.SignerIdentity,
	})
}

// solutionDeliveryStatus maps a refusal onto its response code.
//
// This mapping IS the published contract, and it is written as one function so
// the taxonomy is readable in one place rather than inferred from scattered
// returns. The organising question is not severity but **what a retry would
// change**:
//
//	400 malformed            terminal — the bytes are wrong
//	401 carrier refused      terminal — the token is kubelet-projected and
//	                         re-read per request, so a refusal is a
//	                         configuration fault and a retry asks the same
//	                         question
//	403 not the writer       terminal — wrong service account or namespace
//	403 unattested           terminal — no accepted signer
//	403 domain not granted   terminal — accepted signer, wrong authority
//	409 rewritten generation terminal — publish never rewrites one, so this is
//	                         a hand-edited delivery tree
//	422 invalid document     terminal — it parses and Validate refuses it
//	503 cannot verify        RETRYABLE — the host could not reach the API
//	                         server or its trust root, so nothing was checked
//
// The 401/503 split is the one that matters most and the one an earlier draft
// got wrong by grouping on HTTP class: "the review ran and refused" and "the
// review could not run" are different facts with opposite retry answers, and
// they come from different branches of the same call.
func solutionDeliveryStatus(err error) (int, string) {
	switch {
	case errors.Is(err, business.ErrSolutionDeliveryMalformed):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, infra.ErrCarrierUnauthenticated):
		return http.StatusUnauthorized, err.Error()
	case errors.Is(err, infra.ErrCarrierNotAuthorized):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryUnattested):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryDomainNotGranted):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryRewritten):
		return http.StatusConflict, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryInvalid):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, infra.ErrKubernetesUnavailable):
		return http.StatusServiceUnavailable, err.Error()
	}
	// Anything unclassified is 503 rather than 500, deliberately. An unmapped
	// failure means the host does not know whether it verified the carrier, and
	// the honest answer to "did you accept this?" is "ask again" rather than a
	// 500 that a Job's retry policy treats as terminal.
	return http.StatusServiceUnavailable, "delivery could not be completed"
}

func writeSolutionDeliveryError(w http.ResponseWriter, status int, message string) {
	writeSolutionDeliveryJSON(w, status, solutionDeliveryReceipt{Error: message})
}

func writeSolutionDeliveryJSON(w http.ResponseWriter, status int, body solutionDeliveryReceipt) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
