package business_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

// countingCipher is purposeCipher that counts every round trip to the secret
// provider, which is how a test proves a public source never reached it.
type countingCipher struct {
	purposeCipher
	mu       sync.Mutex
	encrypts int
	decrypts int
}

func (c *countingCipher) EncryptSecret(ctx context.Context, purpose, plaintext string) (string, error) {
	c.mu.Lock()
	c.encrypts++
	c.mu.Unlock()
	return c.purposeCipher.EncryptSecret(ctx, purpose, plaintext)
}

func (c *countingCipher) DecryptSecret(ctx context.Context, purpose, envelope string) (string, error) {
	c.mu.Lock()
	c.decrypts++
	c.mu.Unlock()
	return c.purposeCipher.DecryptSecret(ctx, purpose, envelope)
}

func (c *countingCipher) calls() (encrypts, decrypts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.encrypts, c.decrypts
}

type publicHarness struct {
	svc      *business.Service
	store    *datasourceFakeStore
	cipher   *countingCipher
	audit    *recordingAudit
	producer *recordingProducer
	gh       *fakeGitHub
	tokens   *githubTokens
}

// newPublicHarness is a deployment with no GitHub App: the only credential-less
// path open is a public read.
func newPublicHarness(t *testing.T, gh *fakeGitHub) *publicHarness {
	t.Helper()
	store := newDatasourceFakeStore()
	producer := &recordingProducer{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	cipher := &countingCipher{}
	svc.SetDatasourceConnector(cipher, producer, "")
	audit := &recordingAudit{}
	svc.SetAuditEmitter(audit)
	tokens := &githubTokens{}
	svc.SetDatasourceGitHubClientFactory(func(token string) business.GitHubContentClient {
		tokens.record(token)
		return gh
	})
	return &publicHarness{svc: svc, store: store, cipher: cipher, audit: audit, producer: producer, gh: gh, tokens: tokens}
}

func (h *publicHarness) stored(t *testing.T, id string) business.DatasourceSource {
	t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	source, ok := h.store.sources[id]
	require.True(t, ok, "source %s was not persisted", id)
	return *source
}

func (h *publicHarness) sourceCount() int {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return len(h.store.sources)
}

// requireDeclaredPayloads runs the production payload check over every recorded
// audit entry. The durable emitter only warns and then drops a payload carrying
// an undeclared field, so nothing else would notice.
func requireDeclaredPayloads(t *testing.T, audit *recordingAudit) {
	t.Helper()
	audit.mu.Lock()
	defer audit.mu.Unlock()
	for _, entry := range audit.entries {
		require.NoError(t, business.ValidatePayload(entry.EventType, entry.Payload), "event %s", entry.EventType)
	}
}

func publicInput() business.AddGitHubSourceInput {
	return business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/handbook", CollectionLabel: "handbook"}
}

func TestAddGitHubSource_PublicRepositoryNeedsNoCredential(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "abc", public: true})

	source, err := h.svc.AddGitHubSource(context.Background(), "actor-1", publicInput())
	require.NoError(t, err)

	stored := h.stored(t, source.ID)
	require.Equal(t, "public", stored.GitHubCredentialKind, "the credential-less kind must be recorded, not inferred")
	require.Empty(t, stored.CredentialSecretRef, "a public source stores no credential envelope")
	require.Empty(t, stored.WebhookSecretRef, "a public source registers no webhook")
	require.False(t, stored.WebhookConfigured())
	require.Empty(t, stored.GitHubInstallationID)
	require.NotNil(t, stored.NextReconcileAt, "a public source is kept current by the periodic reconcile")

	encrypts, _ := h.cipher.calls()
	require.Zero(t, encrypts, "connecting a public repository must not reach the secret provider")
	require.Equal(t, []string{""}, uniqueTokens(h.tokens.all()), "visibility and access were proven unauthenticated")

	added := h.audit.entriesOf(business.EventDatasourceSourceAdded)
	require.Len(t, added, 1)
	require.Equal(t, "public", added[0].Payload["credential_kind"])
	require.Equal(t, "acme/handbook", added[0].Payload["repo"])
	requireDeclaredPayloads(t, h.audit)
}

func TestAddGitHubSource_PublicRepositoryRefusesAWebhookSecret(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "abc", public: true})
	input := publicInput()
	input.WebhookSecret = "whsec"

	_, err := h.svc.AddGitHubSource(context.Background(), "actor-1", input)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "err = %v", err)
	require.Zero(t, h.sourceCount())
	encrypts, _ := h.cipher.calls()
	require.Zero(t, encrypts)
}

// A private and a missing repository look the same to an unauthenticated
// request, and neither may be connected without a credential.
func TestAddGitHubSource_PrivateRepositoryStillNeedsACredential(t *testing.T) {
	cases := map[string]*fakeGitHub{
		"reported private":   {defaultBranch: "main", commit: "abc", public: false},
		"private or missing": {defaultBranch: "main", commit: "abc", publicErr: github.ErrNotFound},
	}
	for name, gh := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPublicHarness(t, gh)
			_, err := h.svc.AddGitHubSource(context.Background(), "actor-1", publicInput())
			require.Equal(t, codes.FailedPrecondition, status.Code(err), "err = %v", err)
			require.Contains(t, status.Convert(err).Message(), "PAT")
			require.Zero(t, h.sourceCount())
			require.Empty(t, h.audit.entriesOf(business.EventDatasourceSourceAdded))
		})
	}
}

func TestAddGitHubSource_UnauthenticatedRateLimitIsActionable(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{publicErr: github.ErrUnauthenticatedRateLimited})

	_, err := h.svc.AddGitHubSource(context.Background(), "actor-1", publicInput())
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "err = %v", err)
	message := status.Convert(err).Message()
	require.Contains(t, message, "60 an hour")
	require.Contains(t, message, "PAT")
	require.Zero(t, h.sourceCount())
}

func TestAddSource_PublicGitHubRepositoryNeedsNoCredential(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "abc", public: true})

	source, err := h.svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
		OrgID: testOrg, Provider: business.DatasourceProviderGitHub, Repo: "acme/handbook", CollectionLabel: "handbook",
	})
	require.NoError(t, err)
	stored := h.stored(t, source.ID)
	require.Equal(t, "public", stored.GitHubCredentialKind)
	require.Empty(t, stored.CredentialSecretRef)
	encrypts, _ := h.cipher.calls()
	require.Zero(t, encrypts)

	added := h.audit.entriesOf(business.EventDatasourceSourceAdded)
	require.Len(t, added, 1)
	require.Equal(t, "public", added[0].Payload["credential_kind"])
	require.Equal(t, "github", added[0].Payload["provider"])
	requireDeclaredPayloads(t, h.audit)
}

// The App is preferred whenever an installation this organization claimed
// covers the repository, public or not: its token carries the installation's
// limit rather than the unauthenticated one the host's IP shares.
func TestAddGitHubSource_PrefersTheAppOverAPublicRead(t *testing.T) {
	h := newAppHarness(t, &fakeGitHubApp{})
	h.claimInstallation(t, testOrg)
	gh := &fakeGitHub{defaultBranch: "main", commit: "abc", public: true}
	h.svc.SetDatasourceGitHubClientFactory(func(token string) business.GitHubContentClient {
		h.tokens.record(token)
		return gh
	})

	source, err := h.svc.AddGitHubSource(context.Background(), "actor-1", publicInput())
	require.NoError(t, err)

	require.Empty(t, source.GitHubCredentialKind)
	require.Contains(t, h.storedCredential(t, source.ID), `"kind":"app"`)
	for _, token := range h.tokens.all() {
		require.NotEmpty(t, token, "an App-covered repository must never be read unauthenticated")
	}
	added := h.audit.entriesOf(business.EventDatasourceSourceAdded)
	require.Len(t, added, 1)
	require.Equal(t, "app", added[0].Payload["credential_kind"])
	requireDeclaredPayloads(t, h.audit)
}

func TestAddGitHubSource_FallsBackToAPublicReadOnlyWhenTheAppDeclines(t *testing.T) {
	cases := map[string]struct {
		app     *fakeGitHubApp
		claim   bool
		public  bool
		want    string
		refusal codes.Code
	}{
		"not installed, public":               {app: &fakeGitHubApp{lookupStatus: 404}, public: true, want: "public"},
		"installed but unclaimed, public":     {app: &fakeGitHubApp{}, public: true, want: "public"},
		"not installed, private":              {app: &fakeGitHubApp{lookupStatus: 404}, refusal: codes.FailedPrecondition},
		"installed but unclaimed, private":    {app: &fakeGitHubApp{}, refusal: codes.PermissionDenied},
		"app lookup failing, public":          {app: &fakeGitHubApp{lookupStatus: 500}, public: true},
		"claimed installation denies, public": {app: &fakeGitHubApp{mintStatus: 403}, claim: true, public: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAppHarness(t, tc.app)
			if tc.claim {
				h.claimInstallation(t, testOrg)
			}
			gh := &fakeGitHub{defaultBranch: "main", commit: "abc", public: tc.public}
			h.svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return gh })

			source, err := h.svc.AddGitHubSource(context.Background(), "actor-1", publicInput())
			switch {
			case tc.want != "":
				require.NoError(t, err)
				require.Equal(t, tc.want, source.GitHubCredentialKind)
				require.Empty(t, source.CredentialSecretRef)
			case tc.refusal != codes.OK:
				require.Equal(t, tc.refusal, status.Code(err), "err = %v", err)
			default:
				// An App failure that is not a plain "does not cover this
				// repository" ends the connect: silently settling for an
				// unauthenticated read would make a transient outage permanent.
				require.Error(t, err)
				require.Nil(t, source)
			}
		})
	}
}

// publicSource connects a public repository and returns it as the store holds it.
func (h *publicHarness) publicSource(t *testing.T) *business.DatasourceSource {
	t.Helper()
	source, err := h.svc.AddGitHubSource(context.Background(), "actor-1", publicInput())
	require.NoError(t, err)
	stored := h.stored(t, source.ID)
	return &stored
}

func TestReconcile_PublicSourceReadsUnauthenticatedWithoutTheSecretProvider(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{
		defaultBranch: "main", commit: "HEAD", public: true,
		files: []github.File{{Path: "docs/a.md", SHA: "sa"}},
	})
	source := h.publicSource(t)
	before := len(h.tokens.all())

	enqueued, err := h.svc.ReconcileGitHubSource(context.Background(), source, true, "")
	require.NoError(t, err)
	require.True(t, enqueued)
	require.Len(t, h.producer.jobs, 1)

	used := h.tokens.all()[before:]
	require.NotEmpty(t, used)
	require.Equal(t, []string{""}, uniqueTokens(used))
	encrypts, decrypts := h.cipher.calls()
	require.Zero(t, encrypts)
	require.Zero(t, decrypts, "a public source has no envelope to open")
}

// A missing envelope is only "public" when the source says so. An empty field
// on its own — a lost write, a hand-edited row — is refused rather than read as
// permission to sync unauthenticated; so is a source claiming both.
func TestReconcile_RefusesAnInconsistentCredentialRecord(t *testing.T) {
	cases := map[string]func(*business.DatasourceSource){
		"no envelope, no record":  func(s *business.DatasourceSource) { s.GitHubCredentialKind = "" },
		"envelope and public too": func(s *business.DatasourceSource) { s.CredentialSecretRef = "enc:x" },
		"unknown recorded kind":   func(s *business.DatasourceSource) { s.GitHubCredentialKind = "anonymous" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true})
			source := h.publicSource(t)
			mutate(source)

			_, err := h.svc.ReconcileGitHubSource(context.Background(), source, true, "")
			var failure *jobs.ProcessingError
			require.ErrorAs(t, err, &failure)
			require.False(t, failure.Retryable)
			require.Equal(t, "datasource.credential_unreadable", failure.Failure.Code)
			require.Empty(t, h.producer.jobs)
		})
	}
}

// rateLimitedGitHub is a repository GitHub will not serve any more this hour.
type rateLimitedGitHub struct{ *fakeGitHub }

func (rateLimitedGitHub) ResolveCommit(context.Context, string, string) (string, error) {
	return "", github.ErrUnauthenticatedRateLimited
}

func TestSyncJob_UnauthenticatedRateLimitIsActionableAndRetryable(t *testing.T) {
	gh := &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true}
	h := newPublicHarness(t, gh)
	source := h.publicSource(t)
	h.svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient {
		return rateLimitedGitHub{gh}
	})

	err := h.svc.NewDatasourceDeliveryJobHandler()(context.Background(), &jobsv1.JobEnvelope{
		Id:         "job-1",
		Queue:      business.DatasourceDeliveryQueue,
		Topic:      business.DatasourceReconcileTopic,
		Attributes: map[string]string{"datasource.source_id": source.ID},
	})
	var failure *jobs.ProcessingError
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.Retryable, "the limit resets within the hour")
	require.Equal(t, "datasource.github_unauthenticated_rate_limited", failure.Failure.Code)
	require.Contains(t, failure.Failure.Message, "60 an hour")

	failed := h.audit.entriesOf(business.EventDatasourceSyncFailed)
	require.Len(t, failed, 1)
	require.Equal(t, "datasource.github_unauthenticated_rate_limited", failed[0].Payload["code"])
	requireDeclaredPayloads(t, h.audit)
}

func TestSyncNow_PublicSourceRateLimitedIsReportedAsSuch(t *testing.T) {
	gh := &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true}
	h := newPublicHarness(t, gh)
	source := h.publicSource(t)
	h.svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient {
		return rateLimitedGitHub{gh}
	})

	_, err := h.svc.SyncDatasourceSource(context.Background(), "actor-1", testOrg, source.ID)
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "err = %v", err)
	failed := h.audit.entriesOf(business.EventDatasourceSyncFailed)
	require.Len(t, failed, 1)
	require.Equal(t, "datasource.github_unauthenticated_rate_limited", failed[0].Payload["code"])
	require.True(t, strings.Contains(failed[0].Payload["reason"].(string), "60 an hour"))
}

// Supplying a PAT to a public source makes it a PAT source: the envelope is
// written and the credential-less record goes with it, in the same write.
func TestSyncNow_ReplacementPATTurnsAPublicSourceIntoAPATSource(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true})
	source := h.publicSource(t)

	_, err := h.svc.SyncDatasourceSource(context.Background(), "actor-1", testOrg, source.ID, "ghp_new")
	require.NoError(t, err)

	stored := h.stored(t, source.ID)
	require.Empty(t, stored.GitHubCredentialKind)
	require.Equal(t, "enc:"+business.DatasourceConnectorSecretPurpose(source.ID)+":ghp_new", stored.CredentialSecretRef)

	before := len(h.tokens.all())
	_, err = h.svc.ReconcileGitHubSource(context.Background(), &stored, true, "")
	require.NoError(t, err)
	require.Equal(t, []string{"ghp_new"}, uniqueTokens(h.tokens.all()[before:]))
}

func uniqueTokens(tokens []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, token := range tokens {
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}
