package datasource

// Inbound GitHub webhook receipt.
//
// The public HTTP path has one responsibility: verify the exact request body
// against the per-source signing secret and durably persist the raw, verified
// delivery into the generic jobs inbox. It never fetches from GitHub, parses the
// push, or normalizes changes. Once the inbox transaction commits it can safely
// return 2xx; the documents ingest service leases the retained delivery off the
// inbox seam and projects it into change events independently.
//
// This generalizes the Stripe receiver (pkg/billing/handler.go): same
// MaxBytesReader → verify signature → enqueue to JOB_DIRECTION_INBOX → 2xx
// shape. It differs in two ways. GitHub's X-Hub-Signature-256 is a bare
// HMAC-SHA256 over the raw body with no timestamp component. And the retained
// payload is the exact raw delivery bytes with routing metadata in attributes,
// not a service-owned proto projection — the consumer is a different service, so
// the seam stays decoupled from any accounts type.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
)

// GitHubWebhookPath is the route prefix the receiver is mounted at. The trailing
// path segment is the datasource source id, so one endpoint serves every
// registered source.
const GitHubWebhookPath = "/v1/datasource/github/webhook/"

const (
	GitHubWebhookQueue         = "datasource"
	GitHubWebhookTopic         = "datasource.github.push"
	GitHubWebhookSource        = "github.webhook"
	GitHubWebhookSchemaVersion = 1
	GitHubWebhookMaxAttempts   = 24
	gitHubWebhookContentType   = "application/json"

	// GitHub caps webhook payloads at 25 MiB, but the generic inbox retains at
	// most 1 MiB per job. A delivery whose verified body exceeds this bound is
	// rejected; the datasource's API client re-fetches those refs by SHA rather
	// than trusting an oversized webhook body.
	maxGitHubWebhookBody = 960 * 1024

	deliveryHeader  = "X-GitHub-Delivery"
	eventHeader     = "X-GitHub-Event"
	signatureHeader = "X-Hub-Signature-256"

	attrEvent    = "github.event"
	attrSourceID = "datasource.source_id"
)

// ErrSourceNotFound reports that no signing secret is registered for the source
// named in the webhook path.
var ErrSourceNotFound = errors.New("datasource: no signing secret for source")

// SigningSecretResolver returns the HMAC signing secret registered for one
// datasource. It is the receipt-time seam to the per-source credential store:
// today an in-memory map, later the Vault-transit encrypted store.
type SigningSecretResolver interface {
	SigningSecret(ctx context.Context, sourceID string) (string, error)
}

// HandlerDeps are deliberately limited to receipt-time dependencies.
type HandlerDeps struct {
	Producer jobs.Producer
	Secrets  SigningSecretResolver
}

// NewHandler returns the fast, public GitHub webhook endpoint mounted at
// pathPrefix.
func NewHandler(pathPrefix string, deps HandlerDeps) http.Handler {
	return &handler{prefix: strings.TrimSuffix(pathPrefix, "/") + "/", deps: deps}
}

type handler struct {
	prefix string
	deps   HandlerDeps
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	log := wool.Get(r.Context()).In("datasource.github.webhook")

	sourceID, ok := strings.CutPrefix(r.URL.Path, h.prefix)
	if !ok || sourceID == "" || strings.Contains(sourceID, "/") {
		// An unroutable path is answered exactly like a signature failure so the
		// endpoint reveals nothing about which source ids exist (see below).
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	// Resolve the secret before reading the body so an unknown source is rejected
	// without draining a potentially large request. An unknown source, a resolver
	// error, and a genuine signature mismatch all return an identical 401
	// response — same status, same body — so the endpoint cannot be probed to
	// enumerate configured sources. Only a small timing difference remains
	// (unknown sources skip the body read), which is the accepted cost of not
	// draining unauthenticated bodies.
	secret, err := h.deps.Secrets.SigningSecret(r.Context(), sourceID)
	if err != nil {
		if !errors.Is(err, ErrSourceNotFound) {
			log.Warn("resolve signing secret failed", wool.ErrField(err))
		}
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitHubWebhookBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if err := verifySignature(body, r.Header.Get(signatureHeader), secret); err != nil {
		log.Warn("signature verification failed", wool.ErrField(err))
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	deliveryID := strings.TrimSpace(r.Header.Get(deliveryHeader))
	event := strings.TrimSpace(r.Header.Get(eventHeader))
	if deliveryID == "" || event == "" {
		writeError(w, http.StatusBadRequest, "missing delivery headers")
		return
	}

	response, err := h.deps.Producer.EnqueueJob(r.Context(), &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			Queue:          GitHubWebhookQueue,
			Topic:          GitHubWebhookTopic,
			Source:         GitHubWebhookSource,
			IdempotencyKey: deliveryID,
			SchemaVersion:  GitHubWebhookSchemaVersion,
			Payload:        body,
			ContentType:    gitHubWebhookContentType,
			MaxAttempts:    GitHubWebhookMaxAttempts,
			Attributes: map[string]string{
				attrEvent:    event,
				attrSourceID: sourceID,
			},
		},
	})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "delivery conflict")
			return
		}
		if errors.Is(err, jobs.ErrInvalidCommand) {
			// Caller-fault input (e.g. a delivery id past the durable key bound):
			// redelivering the same request will always fail, so answer 4xx.
			log.Warn("reject invalid webhook command", wool.ErrField(err))
			writeError(w, http.StatusBadRequest, "invalid delivery")
			return
		}
		// The delivery was not durably recorded. GitHub does not automatically
		// retry a non-2xx, so recovery is an operator/API redelivery or the
		// connector's periodic re-sync; a 5xx at least records it as failed
		// rather than falsely acknowledging an unpersisted delivery.
		log.Warn("persist webhook failed", wool.ErrField(err))
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	switch response.GetDisposition() {
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE:
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED:
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
	default:
		log.Warn("persist webhook returned no durable disposition")
		writeError(w, http.StatusInternalServerError, "internal")
	}
}

// verifySignature checks GitHub's X-Hub-Signature-256 scheme: HMAC-SHA256 over
// the exact raw body, hex-encoded and prefixed "sha256=". Unlike Stripe there is
// no timestamp component. The comparison is constant-time.
func verifySignature(body []byte, header, secret string) error {
	if secret == "" {
		return errors.New("datasource: signing secret not configured")
	}
	sig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok || sig == "" {
		return errors.New("datasource: missing or malformed signature header")
	}
	provided, err := hex.DecodeString(sig)
	if err != nil {
		return errors.New("datasource: signature is not valid hex")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("datasource: signature mismatch")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
