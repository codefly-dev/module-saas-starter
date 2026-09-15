package datasource

// Inbound GitHub App receipt: installation lifecycle, and content pushes.
//
// A second receiver beside the per-source push endpoint, deliberately not a
// branch inside it. A GitHub App has exactly one webhook URL and exactly one
// webhook secret, both set on the registration, while the per-source receiver
// verifies a secret the tenant pasted in for its own repository — so nothing
// delivered to the App can be verified there. For `installation` and
// `installation_repositories` that is doubly true: they have GitHub
// availability "app" and cannot be subscribed to on a repository hook at all.
//
// Two kinds of delivery arrive here, and the receiver treats both the same way
// — verify, durably record, return 2xx — while recording different facts.
//
// A lifecycle delivery is a claim that something about an installation changed.
// The receiver does not act on it: it records which installation to re-examine,
// that routing fact alone and never the delivery body, and the leased
// reconciler re-derives each affected source's access from GitHub. A delivery
// can be replayed, delayed or arrive out of order, and revoking a tenant's
// source on a stale claim is worse than acting a beat later.
//
// A `push` delivery is the content half (issue #734). An App-backed source has
// no push secret of its own, so this is the only endpoint that can carry its
// pushes. The push names an installation and a repository but no source and no
// tenant, and one installation may serve authorized sources in several
// tenants — so the receiver records ONE delivery and the leased fan-out
// resolves who is currently eligible, binding each source's own tenant,
// boundary, branch and path scope from its row. Receipt neither reads the push
// body nor fans out on the request, so a fan-out interrupted halfway is retried
// from the durable delivery rather than lost with the request.
//
// Webhooks only improve latency here. "Sync now" and the periodic reconcile
// remain the correctness and recovery path: a missed, truncated or
// out-of-order push converges through authenticated source reconciliation, and
// a push list is never treated as a complete inventory.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
)

// GitHubAppWebhookPath is the route the App registration's webhook URL points
// at. Unlike the per-source receiver it takes no path parameter: one App has
// one webhook URL, and each delivery names its installation in the body.
const GitHubAppWebhookPath = "/v1/datasource/github/app/webhook"

const (
	// GitHubAppWebhookQueue is the accounts-owned queue the installation
	// reconciler leases. It is separate from the push delivery queue because its
	// jobs are keyed by installation rather than by source: one delivery can
	// concern many sources across many tenants. It must match
	// business.DatasourceInstallationQueue — this package is imported by
	// business, so the constant cannot be shared without an import cycle.
	GitHubAppWebhookQueue         = "datasource.installations"
	GitHubAppWebhookTopic         = "datasource.github.installation"
	GitHubAppWebhookSource        = "github.app.webhook"
	GitHubAppWebhookSchemaVersion = 1
	GitHubAppWebhookMaxAttempts   = 24

	// installationOrderingNamespace serializes work per installation, so a
	// suspend and the unsuspend that follows it reconcile in the order GitHub
	// sent them instead of racing — and so the host's own re-check cannot run
	// beside a delivery for the same installation. It must match
	// business.datasourceInstallationOrderingNamespace, which the re-check sweep
	// stamps on the jobs it enqueues.
	installationOrderingNamespace = "datasource.installation"

	attrInstallationID = "datasource.installation_id"

	// attrRepo names the repository a content delivery concerns. It must match
	// business.attrRepo, which the fan-out reads it back from.
	attrRepo = "github.repo"

	// GitHubAppPushTopic marks a verified App-level content delivery awaiting
	// fan-out. It is accepted onto the existing per-source delivery queue
	// (GitHubWebhookQueue) rather than a queue of its own — no new queue, and
	// the compiler's worker already leases it — but carries its own topic,
	// because the job is a fan-out request and not yet any source's delivery.
	// It must match business.datasourceAppPushTopic.
	GitHubAppPushTopic = "datasource.github.app_push"
)

// The App-level events this receiver acts on. `installation` covers
// created/deleted/suspend/unsuspend and `installation_repositories` the
// repository selection a tenant edits; both change which repositories a source
// may read. `push` is the content event, and the registration must subscribe to
// it for App-backed sources to receive live content.
const (
	installationEvent             = "installation"
	installationRepositoriesEvent = "installation_repositories"
	pushEvent                     = "push"
)

// AppWebhookSecretResolver hands the receiver the App registration's webhook
// secret. It is one deployment-wide value, not a per-source lookup, so it costs
// no database read on the request path.
type AppWebhookSecretResolver interface {
	AppWebhookSecret(ctx context.Context) (string, error)
}

// AppHandlerDeps are deliberately limited to receipt-time dependencies.
type AppHandlerDeps struct {
	Producer     jobs.Producer
	Registration AppWebhookSecretResolver
}

// NewAppHandler returns the public App-level webhook endpoint.
func NewAppHandler(deps AppHandlerDeps) http.Handler {
	return &appHandler{deps: deps}
}

type appHandler struct {
	deps AppHandlerDeps
}

func (h *appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	log := wool.Get(r.Context()).In("datasource.github.app.webhook")

	secret, err := h.deps.Registration.AppWebhookSecret(r.Context())
	if err != nil {
		log.Warn("resolve app webhook secret failed", wool.ErrField(err))
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

	// An App receives every event its registration subscribes to, including the
	// setup ping. Only the events below are acted on; the rest are acknowledged
	// so GitHub does not retry them.
	var job *jobsv1.NewJob
	switch event {
	case installationEvent, installationRepositoriesEvent:
		job, err = lifecycleJob(body, event, deliveryID)
	case pushEvent:
		job, err = contentJob(body, event, deliveryID)
	default:
		log.Info("ignoring unsubscribed event", wool.Field("event", event))
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if err != nil {
		// Every event handled here names its installation, and a push names its
		// repository too. A verified delivery missing one cannot be routed, and
		// will not route on redelivery either.
		log.Warn("delivery cannot be routed", wool.Field("event", event), wool.ErrField(err))
		writeError(w, http.StatusBadRequest, "invalid delivery")
		return
	}

	response, err := h.deps.Producer.EnqueueJob(r.Context(), &jobsv1.EnqueueJobRequest{Job: job})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "delivery conflict")
			return
		}
		if errors.Is(err, jobs.ErrInvalidCommand) {
			log.Warn("reject invalid webhook command", wool.ErrField(err))
			writeError(w, http.StatusBadRequest, "invalid delivery")
			return
		}
		// The delivery was not durably recorded. A 5xx records it as failed on
		// the App's deliveries page, where an operator can redeliver it, rather
		// than acknowledging a revocation this host never persisted.
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

// lifecycleJob is the reconcile request an `installation` or
// `installation_repositories` delivery produces.
//
// Only the routing fact is retained, never the delivery body. The reconciler
// re-derives every source's access from GitHub and reads nothing out of the
// payload, so keeping the original would durably store a third party's
// repository list, account and sender for no consumer at all. A replay needs
// the installation id and nothing else. Marshalling a one-entry map of strings
// has no failure mode, which is why no error is plumbed out of it.
func lifecycleJob(body []byte, event, deliveryID string) (*jobsv1.NewJob, error) {
	installationID, _, err := deliveryRouting(body)
	if err != nil {
		return nil, err
	}
	retained, _ := json.Marshal(map[string]string{"installation_id": installationID})
	return &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     GitHubAppWebhookQueue,
		Topic:     GitHubAppWebhookTopic,
		Source:    GitHubAppWebhookSource,
		Ordering: &jobsv1.JobOrderingKey{
			Namespace:  installationOrderingNamespace,
			Components: []string{installationID},
		},
		IdempotencyKey: deliveryID,
		SchemaVersion:  GitHubAppWebhookSchemaVersion,
		Payload:        retained,
		ContentType:    gitHubWebhookContentType,
		MaxAttempts:    GitHubAppWebhookMaxAttempts,
		Attributes: map[string]string{
			attrEvent:          event,
			attrInstallationID: installationID,
			attrDeliveryID:     deliveryID,
		},
	}, nil
}

// contentJob is the single fan-out request a verified `push` produces.
//
// Unlike a lifecycle delivery, the body IS retained — it is the push the
// change-set compiler consumes once the fan-out has resolved which sources it
// concerns, and GitHub offers no way to re-fetch a delivery. It is retained
// exactly as received, so the bytes any source is compiled from are the bytes
// the signature covered.
//
// One delivery is accepted, not one per source: the fan-out is the retryable
// half. The idempotency key is therefore the original delivery id alone, which
// is stable across redelivery, and the per-source keys the fan-out derives from
// it are what keep each source's copy distinct.
//
// The ordering key names the installation and repository because no source is
// known yet. It shares the per-source delivery namespace, so two pushes to one
// repository fan out in the order GitHub sent them, and the per-source
// deliveries they produce inherit that order behind each source's own key.
func contentJob(body []byte, event, deliveryID string) (*jobsv1.NewJob, error) {
	installationID, repo, err := deliveryRouting(body)
	if err != nil {
		return nil, err
	}
	if repo == "" {
		return nil, errors.New("datasource: push delivery carries no repository")
	}
	return &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     GitHubWebhookQueue,
		Topic:     GitHubAppPushTopic,
		Source:    GitHubAppWebhookSource,
		Ordering: &jobsv1.JobOrderingKey{
			Namespace:  deliveryOrderingNamespace,
			Components: []string{"app", installationID, repo},
		},
		IdempotencyKey: deliveryID,
		SchemaVersion:  GitHubWebhookSchemaVersion,
		Payload:        body,
		ContentType:    gitHubWebhookContentType,
		MaxAttempts:    GitHubWebhookMaxAttempts,
		Attributes: map[string]string{
			attrEvent:          event,
			attrInstallationID: installationID,
			attrRepo:           repo,
			attrDeliveryID:     deliveryID,
		},
	}, nil
}

// deliveryRouting reads the facts a verified delivery is routed by: the
// installation it concerns, and the repository when it names one.
//
// This is routing, not authority. The reconciler re-derives each source's
// access from GitHub and the fan-out re-reads eligibility from the source rows
// themselves, rather than believing the body. GitHub reports the installation
// id as a JSON number, and json.Number keeps a 64-bit id exact rather than
// routing it through float64.
func deliveryRouting(body []byte) (string, string, error) {
	var envelope struct {
		Installation struct {
			ID json.Number `json:"id"`
		} `json:"installation"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", "", err
	}
	if envelope.Installation.ID.String() == "" {
		return "", "", errors.New("datasource: delivery carries no installation id")
	}
	return envelope.Installation.ID.String(), strings.TrimSpace(envelope.Repository.FullName), nil
}
