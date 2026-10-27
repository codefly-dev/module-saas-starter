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

	"github.com/codefly-dev/core/wool"
)

// The delivery endpoint: `POST /platform/_delivery/manifest`, one signed module
// manifest carrier per request, `POST /platform/_delivery/envelope`, one
// release-signed module envelope carrier, and
// `GET /platform/_delivery/applied/<binding>`.
//
// Mounted on ACCOUNTS rather than brokered through the gateway, and the
// asymmetry with the credential mint is deliberate. `POST
// /platform/_credential` is brokered because a solution runtime is an
// independently deployed workload that must not reach accounts' internal
// listener. A delivery Job is not that: it is in-cluster and runs as its
// module's `delivery` ServiceAccount, so routing it through the edge would put
// accounts-audience tokens across the perimeter for no gain.
//
// `/platform/` is RESERVED for the host, and an unknown `/platform/*` path
// answers 404 rather than falling through. That is a contract rather than
// tidiness: a consumer has to be able to tell "no such endpoint" from "endpoint
// broken", and a fall-through to generic routing answers 502 for the first.

// SolutionDeliveryPrefix is where the delivery endpoints are mounted. Published
// and pinned by the doc-claim gate, so a rename that does not move the document
// breaks every consumer silently.
const SolutionDeliveryPrefix = "/platform/_delivery/"

// SolutionDeliveryManifestPath is the one delivery write: a module manifest.
const SolutionDeliveryManifestPath = SolutionDeliveryPrefix + "manifest"

// SolutionDeliveryEnvelopePath is the other delivery write: the host's module
// envelope, signed by a release key and carried by the host's own delivery
// account.
const SolutionDeliveryEnvelopePath = SolutionDeliveryPrefix + "envelope"

// solutionDeliveryMaxBytes bounds a carrier body.
//
// A DSSE envelope carries the document again alongside its signature; 1 MiB is
// far above both and far below anything that would cost the host to read. The
// bound exists because this endpoint authenticates the caller only AFTER parsing
// enough to know what was sent, so an unbounded read would be work an
// unauthenticated caller could ask for.
const solutionDeliveryMaxBytes = 1 << 20

type solutionDeliveryService interface {
	ReceiveSolutionDelivery(
		ctx context.Context, raw []byte, credential string,
	) (business.SolutionDeliveryDisposition, *business.SolutionDeliveryRecord, error)
	ReadSolutionDeliveryApplied(context.Context, string, string) (*business.SolutionDeliveryApplied, error)
	ReceiveModuleEnvelopeDelivery(ctx context.Context, raw []byte, credential string) (*business.ModuleEnvelopeDeliveryReceipt, error)
}

// SolutionDeliveryHTTPHandler serves the signed delivery paths.
type SolutionDeliveryHTTPHandler struct {
	service solutionDeliveryService
}

// NewSolutionDeliveryHTTPHandler builds the handler.
func NewSolutionDeliveryHTTPHandler(service *business.Service) http.Handler {
	return &SolutionDeliveryHTTPHandler{service: service}
}

// solutionDeliveryReceipt is what a caller gets back: the disposition and the
// generation the host now holds, or the refusal and its reason. Both successes
// answer 200 — a delivery Job and a native run treat any other code as the
// host's verdict — and the disposition says whether anything changed.
type solutionDeliveryReceipt struct {
	Disposition string `json:"disposition,omitempty"`
	Binding     string `json:"binding,omitempty"`
	Generation  uint64 `json:"generation,omitempty"`
	Revision    uint64 `json:"revision,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Signer      string `json:"signer,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (h *SolutionDeliveryHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, SolutionDeliveryPrefix+"applied/") {
		h.serveApplied(w, r)
		return
	}
	// An unknown path under the reserved namespace is 404, never a fall-through.
	if r.URL.Path != SolutionDeliveryManifestPath && r.URL.Path != SolutionDeliveryEnvelopePath {
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

	if r.URL.Path == SolutionDeliveryEnvelopePath {
		h.serveEnvelope(w, r, body)
		return
	}
	disposition, record, err := h.service.ReceiveSolutionDelivery(r.Context(), body, r.Header.Get("Authorization"))
	if err != nil {
		status, message := solutionDeliveryStatus(err)
		// Every refusal is logged with its reason as well as answered with it:
		// the Job's log and this host's log name the same cause.
		wool.Get(r.Context()).In("solution_delivery").Warn("module manifest delivery refused",
			wool.Field("status", status), wool.Field("reason", message))
		writeSolutionDeliveryError(w, status, message)
		return
	}
	wool.Get(r.Context()).In("solution_delivery").Info("module manifest delivered",
		wool.Field("disposition", string(disposition)), wool.Field("binding", record.Binding),
		wool.Field("generation", record.Generation), wool.Field("signer", record.SignerIdentity))
	writeSolutionDeliveryJSON(w, http.StatusOK, solutionDeliveryReceipt{
		Disposition: string(disposition),
		Binding:     record.Binding,
		Generation:  record.Generation,
		Digest:      record.ContentHash,
		Signer:      record.SignerIdentity,
	})
}

// serveEnvelope answers one delivered module envelope with the manifest's
// receipt shape and status taxonomy; the receipt names the revision.
func (h *SolutionDeliveryHTTPHandler) serveEnvelope(w http.ResponseWriter, r *http.Request, body []byte) {
	receipt, err := h.service.ReceiveModuleEnvelopeDelivery(r.Context(), body, r.Header.Get("Authorization"))
	if err != nil {
		status, message := solutionDeliveryStatus(err)
		wool.Get(r.Context()).In("solution_delivery").Warn("module envelope delivery refused",
			wool.Field("status", status), wool.Field("reason", message))
		writeSolutionDeliveryError(w, status, message)
		return
	}
	wool.Get(r.Context()).In("solution_delivery").Info("module envelope delivered",
		wool.Field("disposition", string(receipt.Disposition)), wool.Field("revision", receipt.Revision),
		wool.Field("signer", receipt.Signer))
	writeSolutionDeliveryJSON(w, http.StatusOK, solutionDeliveryReceipt{
		Disposition: string(receipt.Disposition),
		Revision:    receipt.Revision,
		Digest:      receipt.Digest,
		Signer:      receipt.Signer,
	})
}

func (h *SolutionDeliveryHTTPHandler) serveApplied(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	binding := strings.TrimPrefix(r.URL.Path, SolutionDeliveryPrefix+"applied/")
	if binding == "" || len(binding) > 128 || strings.ContainsAny(binding, "/\\") {
		writeSolutionDeliveryError(w, http.StatusNotFound, "no such delivery endpoint")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeSolutionDeliveryError(w, http.StatusMethodNotAllowed, "applied state is a GET")
		return
	}
	if len(r.Header.Values("Authorization")) != 1 {
		writeSolutionDeliveryError(w, http.StatusUnauthorized, "one observer credential is required")
		return
	}
	applied, err := h.service.ReadSolutionDeliveryApplied(r.Context(), binding, r.Header.Get("Authorization"))
	if errors.Is(err, business.ErrSolutionHostBindingNotDeclared) {
		writeSolutionDeliveryError(w, http.StatusNotFound, "binding has no applied generation")
		return
	}
	if err != nil {
		status, message := solutionDeliveryStatus(err)
		writeSolutionDeliveryError(w, status, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(applied)
}

// solutionDeliveryStatus maps a refusal onto its response code.
//
// This mapping IS the published contract, written as one function so the
// taxonomy is readable in one place. The organising question is **what a retry
// would change**:
//
//	400 malformed            terminal — the bytes are not a canonical manifest
//	                         or envelope, or an envelope grant the host's
//	                         rules refuse (named)
//	401 unattested           terminal — no key this host pins signed it
//	401 carrier refused      terminal — the token is kubelet-projected and
//	                         re-read per request, so a refusal is a
//	                         configuration fault
//	403 not the carrier      terminal — wrong account or namespace
//	403 domain not granted   terminal — the signer or the binding speaks for
//	                         another ownership domain
//	403 not a release key    terminal — an envelope signed by a person key
//	409 conflict             terminal — a stale, rewritten or withdrawn
//	                         generation; a stale or rewritten envelope
//	                         revision, or one at or below a revision an
//	                         administrator wrote interactively (named)
//	422 refused              terminal — another host's manifest, or a reserved
//	                         or held route alias; an envelope for another
//	                         host, or one that names an organization
//	503 host state           RETRYABLE — this host's applied record is unusable
//	503 cannot verify        RETRYABLE — the host could not reach the API
//	                         server, so nothing was checked
//	503 not completed        RETRYABLE — e.g. the committed envelope moved
//	                         during admission
func solutionDeliveryStatus(err error) (int, string) {
	switch {
	case errors.Is(err, business.ErrSolutionDeliveryMalformed):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryUnattested):
		return http.StatusUnauthorized, err.Error()
	case errors.Is(err, infra.ErrCarrierUnauthenticated):
		return http.StatusUnauthorized, err.Error()
	case errors.Is(err, infra.ErrCarrierNotAuthorized):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryDomainNotGranted),
		errors.Is(err, business.ErrSolutionDeliverySignerNotRelease):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryConflict):
		return http.StatusConflict, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryRefused):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, business.ErrSolutionDeliveryHostState):
		return http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, infra.ErrKubernetesUnavailable):
		return http.StatusServiceUnavailable, err.Error()
	}
	// Anything unclassified is 503 rather than 500: the host does not know
	// whether it verified the carrier, and the honest answer is "ask again".
	// The cause is still named, so it is never invisible.
	return http.StatusServiceUnavailable, "delivery could not be completed: " + err.Error()
}

func writeSolutionDeliveryError(w http.ResponseWriter, status int, message string) {
	writeSolutionDeliveryJSON(w, status, solutionDeliveryReceipt{Error: message})
}

func writeSolutionDeliveryJSON(w http.ResponseWriter, status int, body solutionDeliveryReceipt) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
