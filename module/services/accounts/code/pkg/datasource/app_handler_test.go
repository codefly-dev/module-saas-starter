package datasource_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"accounts/pkg/datasource"

	"github.com/stretchr/testify/require"
)

const testAppWebhookSecret = "whsec_github_app_fake_secret"

// staticAppRegistration is the configured App webhook secret, the one value the
// App-level receiver needs at request time.
type staticAppRegistration struct {
	secret string
	err    error
}

func (r staticAppRegistration) AppWebhookSecret(context.Context) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.secret, nil
}

func newAppTestServer(t *testing.T, producer *fakeJobProducer, registration datasource.AppWebhookSecretResolver) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(datasource.GitHubAppWebhookPath, datasource.NewAppHandler(datasource.AppHandlerDeps{
		Producer:     producer,
		Registration: registration,
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

type appDelivery struct {
	event      string
	delivery   string
	body       string
	signWith   string // signs with this secret instead of the configured one
	signature  string // overrides the computed signature entirely
	omitSigned bool   // sends no signature header at all
	method     string
}

func postAppDelivery(t *testing.T, server *httptest.Server, d appDelivery) *http.Response {
	t.Helper()
	if d.method == "" {
		d.method = http.MethodPost
	}
	if d.signWith == "" {
		d.signWith = testAppWebhookSecret
	}
	body := []byte(d.body)
	request, err := http.NewRequest(d.method, server.URL+datasource.GitHubAppWebhookPath, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if d.event != "" {
		request.Header.Set("X-GitHub-Event", d.event)
	}
	if d.delivery != "" {
		request.Header.Set("X-GitHub-Delivery", d.delivery)
	}
	switch {
	case d.omitSigned:
	case d.signature != "":
		request.Header.Set("X-Hub-Signature-256", d.signature)
	default:
		request.Header.Set("X-Hub-Signature-256", signBody(d.signWith, body))
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// A real delivery carries the installation's account and the person who acted,
// neither of which the reconciler reads.
const suspendDelivery = `{"action":"suspend","installation":{"id":4242,"account":{"login":"acme-org"}},"sender":{"login":"example-operator"}}`

// A verified lifecycle delivery is recorded durably and keyed so the reconciler
// can find the installation it concerns — and nothing more: the receiver does
// not call GitHub or touch a source on the request path.
func TestAppWebhookQueuesInstallationDelivery(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		event: "installation", delivery: "delivery-1", body: suspendDelivery,
	})
	require.Equal(t, http.StatusOK, response.StatusCode)

	request, ok := producer.request("delivery-1")
	require.True(t, ok, "a verified delivery must be persisted")
	job := request.GetJob()
	require.Equal(t, datasource.GitHubAppWebhookQueue, job.GetQueue())
	require.Equal(t, datasource.GitHubAppWebhookTopic, job.GetTopic())
	require.Equal(t, "installation", job.GetAttributes()["github.event"])

	// Only the routing fact is retained. The reconciler re-derives everything
	// from GitHub and reads nothing out of the payload, so keeping the delivery
	// would durably store a third party's account and sender for no consumer.
	require.JSONEq(t, `{"installation_id":"4242"}`, string(job.GetPayload()))
	require.NotContains(t, string(job.GetPayload()), "example-operator")
	require.NotContains(t, string(job.GetPayload()), "acme-org")
	require.Equal(t, "4242", job.GetAttributes()["datasource.installation_id"])
	require.Equal(t, "delivery-1", job.GetAttributes()["datasource.delivery_id"])

	// Ordering is per installation, so a suspend and the unsuspend behind it
	// reconcile in the order GitHub sent them rather than racing.
	require.Equal(t, []string{"4242"}, job.GetOrdering().GetComponents())
}

// The tenant-driven half of the lifecycle: editing which repositories an
// installation covers arrives as its own event and must be reconciled too.
func TestAppWebhookQueuesRepositorySelectionDelivery(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		event:    "installation_repositories",
		delivery: "delivery-2",
		body:     `{"action":"removed","installation":{"id":4242},"repositories_removed":[{"full_name":"acme/docs"}]}`,
	})
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, ok := producer.request("delivery-2")
	require.True(t, ok)
}

// The App-wide secret is the only thing that admits a delivery here. A body
// signed with some other secret — a source's own push secret, say — is refused,
// and nothing is recorded.
func TestAppWebhookRejectsForgedSignature(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		event: "installation", delivery: "forged", body: suspendDelivery, signWith: "whsec_some_other_secret",
	})
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.Zero(t, producer.inserts.Load(), "an unverified delivery must never be recorded")
}

func TestAppWebhookRejectsUnsignedDelivery(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		event: "installation", delivery: "unsigned", body: suspendDelivery, omitSigned: true,
	})
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.Zero(t, producer.inserts.Load())
}

// A deployment with no App registration answers exactly like a signature
// failure rather than revealing that it has nothing configured.
func TestAppWebhookRejectsWhenNoSecretIsConfigured(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{err: errors.New("unconfigured")})

	response := postAppDelivery(t, server, appDelivery{
		event: "installation", delivery: "unconfigured", body: suspendDelivery,
	})
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.Zero(t, producer.inserts.Load())
}

// An App receives every event its registration subscribes to. Anything that is
// not one of the two access-changing events is acknowledged so GitHub stops
// retrying it, and is never turned into reconcile work.
func TestAppWebhookAcknowledgesUnrelatedEvent(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	for _, event := range []string{"ping", "installation_target", "github_app_authorization"} {
		response := postAppDelivery(t, server, appDelivery{
			event: event, delivery: "ack-" + event, body: `{"installation":{"id":4242}}`,
		})
		require.Equal(t, http.StatusOK, response.StatusCode, event)
	}
	require.Zero(t, producer.inserts.Load(), "only lifecycle events become reconcile work")
}

// Both lifecycle events always name their installation; a verified delivery
// without one cannot be routed and will not route on redelivery either.
func TestAppWebhookRejectsDeliveryWithoutInstallation(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		event: "installation", delivery: "no-installation", body: `{"action":"deleted"}`,
	})
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Zero(t, producer.inserts.Load())
}

func TestAppWebhookRequiresDeliveryHeaders(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{event: "installation", body: suspendDelivery})
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Zero(t, producer.inserts.Load())
}

// GitHub redelivers on its own and an operator can replay from the App's
// deliveries page; the delivery id keys the job, so a replay is recorded once.
func TestAppWebhookRedeliveryIsIdempotent(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	first := postAppDelivery(t, server, appDelivery{event: "installation", delivery: "same", body: suspendDelivery})
	second := postAppDelivery(t, server, appDelivery{event: "installation", delivery: "same", body: suspendDelivery})
	require.Equal(t, http.StatusOK, first.StatusCode)
	require.Equal(t, http.StatusOK, second.StatusCode)
	require.EqualValues(t, 1, producer.inserts.Load())
}

func TestAppWebhookRejectsNonPost(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		method: http.MethodGet, event: "installation", delivery: "get", body: suspendDelivery,
	})
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
}

// appPushBody is a push as GitHub delivers it to an App: the installation and
// repository that route it, plus the ref and commits that only the compiler
// downstream ever reads.
func appPushBody(installationID, repo string) string {
	return `{"ref":"refs/heads/main","before":"aaa","after":"bbb",` +
		`"installation":{"id":` + installationID + `},` +
		`"repository":{"full_name":"` + repo + `","default_branch":"main"},` +
		`"commits":[{"id":"bbb","modified":["docs/intro.md"]}]}`
}

// A verified push is accepted as ONE durable delivery and nothing more. The
// receiver does not resolve sources, call GitHub or fan out on the request:
// that is the retryable half, and doing it here would lose a partial fan-out
// with the request.
func TestAppWebhookAcceptsContentPush(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	body := appPushBody("4242", "acme/docs")
	response := postAppDelivery(t, server, appDelivery{
		event: "push", delivery: "push-1", body: body,
	})
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.EqualValues(t, 1, producer.inserts.Load(), "one delivery, not one per source")

	request, ok := producer.request("push-1")
	require.True(t, ok, "a verified push must be persisted before it is acknowledged")
	job := request.GetJob()

	// It rides the existing per-source delivery queue — no new queue — under its
	// own topic, because it is a fan-out request and not yet any source's
	// delivery.
	require.Equal(t, datasource.GitHubWebhookQueue, job.GetQueue())
	require.Equal(t, datasource.GitHubAppPushTopic, job.GetTopic())

	// The exact verified bytes are retained: this is the push the compiler
	// consumes once the fan-out resolves who it belongs to, and GitHub offers no
	// way to re-fetch a delivery.
	require.Equal(t, body, string(job.GetPayload()))

	require.Equal(t, "push", job.GetAttributes()["github.event"])
	require.Equal(t, "4242", job.GetAttributes()["datasource.installation_id"])
	require.Equal(t, "acme/docs", job.GetAttributes()["github.repo"])
	require.Equal(t, "push-1", job.GetAttributes()["datasource.delivery_id"])

	// The original delivery id alone keys the durable delivery; the per-source
	// keys are derived from it downstream.
	require.Equal(t, "push-1", job.GetIdempotencyKey())
}

// Ordering is per repository, not per installation: two repositories under one
// installation must not serialize behind each other, while two pushes to the
// same repository must, so the per-source deliveries they produce inherit
// GitHub's order.
func TestAppWebhookPushOrderingIsPerRepository(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	for _, d := range []struct{ delivery, repo string }{
		{"push-docs-1", "acme/docs"},
		{"push-docs-2", "acme/docs"},
		{"push-specs-1", "acme/specs"},
	} {
		response := postAppDelivery(t, server, appDelivery{
			event: "push", delivery: d.delivery, body: appPushBody("4242", d.repo),
		})
		require.Equal(t, http.StatusOK, response.StatusCode, d.delivery)
	}

	ordering := func(delivery string) []string {
		request, ok := producer.request(delivery)
		require.True(t, ok, delivery)
		return request.GetJob().GetOrdering().GetComponents()
	}
	require.Equal(t, []string{"app", "4242", "acme/docs"}, ordering("push-docs-1"))
	require.Equal(t, ordering("push-docs-1"), ordering("push-docs-2"))
	require.NotEqual(t, ordering("push-docs-1"), ordering("push-specs-1"))
}

// GitHub redelivers on its own and an operator can replay from the deliveries
// page. The delivery id keys the job, so a replay cannot become a second
// logical change.
func TestAppWebhookPushRedeliveryIsIdempotent(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	body := appPushBody("4242", "acme/docs")
	first := postAppDelivery(t, server, appDelivery{event: "push", delivery: "same-push", body: body})
	second := postAppDelivery(t, server, appDelivery{event: "push", delivery: "same-push", body: body})
	require.Equal(t, http.StatusOK, first.StatusCode)
	require.Equal(t, http.StatusOK, second.StatusCode)
	require.EqualValues(t, 1, producer.inserts.Load())
}

// A push signed with anything other than the App's own webhook secret — a
// source's push secret, say — cannot produce downstream output.
func TestAppWebhookRejectsForgedPushSignature(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	response := postAppDelivery(t, server, appDelivery{
		event: "push", delivery: "forged-push", body: appPushBody("4242", "acme/docs"),
		signWith: "whsec_some_other_secret",
	})
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.Zero(t, producer.inserts.Load())
}

// A push names its repository and its installation. Verified or not, one
// missing either cannot be routed to a source and will not route on
// redelivery, so it is refused rather than retried forever.
func TestAppWebhookRejectsUnroutablePush(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	for name, body := range map[string]string{
		"no repository":  `{"ref":"refs/heads/main","after":"bbb","installation":{"id":4242}}`,
		"empty repository": `{"ref":"refs/heads/main","after":"bbb","installation":{"id":4242},` +
			`"repository":{"full_name":"  "}}`,
		"no installation": `{"ref":"refs/heads/main","after":"bbb","repository":{"full_name":"acme/docs"}}`,
	} {
		response := postAppDelivery(t, server, appDelivery{
			event: "push", delivery: "unroutable-" + name, body: body,
		})
		require.Equal(t, http.StatusBadRequest, response.StatusCode, name)
	}
	require.Zero(t, producer.inserts.Load())
}

// The body bound applies to a push exactly as it does to a lifecycle delivery:
// the inbox retains at most 1 MiB per job, and an oversized push converges
// through reconciliation instead.
func TestAppWebhookRejectsOversizedPush(t *testing.T) {
	producer := newFakeJobProducer()
	server := newAppTestServer(t, producer, staticAppRegistration{secret: testAppWebhookSecret})

	oversized := `{"ref":"refs/heads/main","after":"bbb","installation":{"id":4242},` +
		`"repository":{"full_name":"acme/docs"},"padding":"` + strings.Repeat("x", 1024*1024) + `"}`
	response := postAppDelivery(t, server, appDelivery{
		event: "push", delivery: "oversized-push", body: oversized,
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
	require.Zero(t, producer.inserts.Load())
}
