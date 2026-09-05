package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
)

// Datasource providers and lifecycle statuses. The provider string is recorded
// on every Source row and selects the connector.
const (
	DatasourceProviderGitHub = "github"
	DatasourceProviderAPI    = "api"

	DatasourceStatusActive = "active"
	DatasourceStatusPaused = "paused"
)

// API credential kinds mirror saas.accounts.v1.ApiCredentialKind; they select
// how the stored credential is presented on the generic connector's requests.
const (
	APICredentialKindBearer = apisource.CredentialKindBearer
	APICredentialKindBasic  = apisource.CredentialKindBasic
	APICredentialKindHeader = apisource.CredentialKindHeader
)

const (
	datasourceConnectorSecretPurposePrefix = "github-connector:"
	datasourceWebhookSecretPurposePrefix   = "github-webhook:"
)

// The ingest seam documents consumes. Sync and the inbound webhook receiver
// (pkg/datasource, issue #275) both land on queue "datasource"; the topic and
// attributes distinguish a full-sync file delivery from a raw push delivery so a
// single leased consumer can route them. The payload is raw bytes with routing
// in attributes — never an accounts-owned proto — because the consumer is a
// different service (documents) and the seam stays decoupled from any accounts
// type.
const (
	datasourceIngestQueue         = "datasource"
	datasourceSyncTopic           = "datasource.github.sync"
	datasourceSyncSource          = "github.sync"
	datasourceIngestSchemaVersion = 1
	datasourceIngestMaxAttempts   = 24
	datasourceIngestContentType   = "application/octet-stream"

	// The internal sync-request queue. SyncSource enqueues one request here and
	// returns; a leased worker performs the actual repo pull off-request, so a
	// large repo cannot block or time out the RPC and gets the jobs framework's
	// retry/backoff (long enough to outlast a GitHub rate-limit window).
	DatasourceSyncRequestQueue         = "datasource.sync"
	datasourceSyncRequestTopic         = "datasource.github.sync.request"
	datasourceSyncRequestSource        = "github.sync.request"
	datasourceSyncRequestSchemaVersion = 1
	datasourceSyncRequestMaxAttempts   = 12

	// A file larger than the generic inbox's ~1 MiB payload cap is skipped by a
	// full sync; the documents ingest step re-fetches such refs by SHA through
	// the connector's API client rather than inlining an oversized payload.
	maxIngestPayload = 960 * 1024

	attrSourceID         = "datasource.source_id"
	attrOrgID            = "datasource.org_id"
	attrTargetCollection = "datasource.target_collection"
	attrRepo             = "github.repo"
	attrPath             = "github.path"
	attrRef              = "github.ref"
	attrSHA              = "github.sha"
	attrCommit           = "github.commit"
	attrChangeType       = "github.change_type"

	// The generic API connector lands on the same shared "datasource" queue; a
	// distinct topic/source lets the documents consumer route an API pull apart
	// from a GitHub one. The fetched body travels as the payload; the resource
	// URL and a content hash travel in attributes.
	datasourceAPISyncTopic  = "datasource.api.sync"
	datasourceAPISyncSource = "api.sync"

	attrAPIURL        = "api.url"
	attrAPIContentSHA = "api.content_sha"

	changeTypeAdded = "added"
)

// ErrDatasourceSourceNotFound reports that no Source (or no webhook-configured
// Source) matches an id. It mirrors the receiver's not-found sentinel so an
// unknown source and an unconfigured one are indistinguishable to a caller.
var ErrDatasourceSourceNotFound = errors.New("datasource: source not found")

// DatasourceConnectorSecretPurpose binds an access-token envelope to one Source
// so ciphertext cannot be replayed across rows. The row id is the binding
// because a repo may be connected more than once.
func DatasourceConnectorSecretPurpose(sourceID string) string {
	return datasourceConnectorSecretPurposePrefix + sourceID
}

// DatasourceWebhookSecretPurpose binds a webhook-signing-secret envelope to one
// Source, same rationale as the access-token purpose.
func DatasourceWebhookSecretPurpose(sourceID string) string {
	return datasourceWebhookSecretPurposePrefix + sourceID
}

// APIDatasourceConfig is the non-secret configuration of a generic
// API-with-credentials Source. It is persisted as the Source row's config JSONB;
// the credential itself lives only as a SecretCipher envelope in
// CredentialSecretRef.
type APIDatasourceConfig struct {
	BaseURL          string `json:"base_url"`
	ResourcePath     string `json:"resource_path"`
	CredentialKind   string `json:"credential_kind"`
	CredentialHeader string `json:"credential_header,omitempty"`
}

// DatasourceSource is one connected external datasource. CredentialSecretRef and
// WebhookSecretRef hold SecretCipher envelopes (references into the secret
// provider), never plaintext. Repo/Paths/Branch are set for the GitHub provider;
// API is set for the generic API provider.
type DatasourceSource struct {
	ID                  string
	OrgID               string
	Provider            string
	Repo                string
	Paths               []string
	Branch              string
	API                 *APIDatasourceConfig
	TargetCollection    string
	CredentialSecretRef string
	WebhookSecretRef    string
	Status              string
	LastSyncedAt        *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// WebhookConfigured reports whether a signing secret is stored, i.e. whether
// live webhook ingestion can verify deliveries for this Source.
func (d *DatasourceSource) WebhookConfigured() bool {
	return strings.TrimSpace(d.WebhookSecretRef) != ""
}

// AddGitHubSourceInput is the caller-supplied configuration for a GitHub Source.
// AccessToken and WebhookSecret are plaintext; each is encrypted through the
// SecretCipher and only its envelope reference is persisted.
type AddGitHubSourceInput struct {
	OrgID            string
	Repo             string
	Paths            []string
	Branch           string
	TargetCollection string
	AccessToken      string
	WebhookSecret    string
}

// GitHubContentClient is the subset of the api.github.com client the datasource
// connector needs. The Service builds one per Source from its decrypted token.
type GitHubContentClient interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	ResolveCommit(ctx context.Context, repo, ref string) (string, error)
	ListFiles(ctx context.Context, repo, ref string, prefixes []string) ([]github.File, error)
	GetFileContent(ctx context.Context, repo, ref, path string) ([]byte, error)
}

// APIContentClient is the subset of the generic API connector the Service needs.
// The Service builds one per Source from its config and decrypted credential.
type APIContentClient interface {
	Fetch(ctx context.Context) (*apisource.Result, error)
}

// SetDatasourceConnector wires the fail-closed credential cipher, the privileged
// inbox producer used for ingest deliveries, and the GitHub API base URL. It is
// the one setter #274 adds; production passes Vault transit and the durable job
// store, tests pass deterministic substitutes.
func (s *Service) SetDatasourceConnector(cipher SecretCipher, producer jobs.Producer, githubBaseURL string) {
	s.datasourceCipher = cipher
	s.datasourceJobs = producer
	s.githubBaseURL = strings.TrimSpace(githubBaseURL)
	if s.newGitHubClient == nil {
		s.newGitHubClient = func(token string) GitHubContentClient {
			return github.New(token, s.githubBaseURL)
		}
	}
	if s.newAPIClient == nil {
		s.newAPIClient = func(cfg APIDatasourceConfig, credential string) APIContentClient {
			return apisource.New(apisource.Config{
				BaseURL:          cfg.BaseURL,
				ResourcePath:     cfg.ResourcePath,
				CredentialKind:   cfg.CredentialKind,
				CredentialHeader: cfg.CredentialHeader,
			}, credential)
		}
	}
}

// SetDatasourceGitHubClientFactory overrides how per-Source GitHub clients are
// built. Tests use it to inject a fake without a live api.github.com.
func (s *Service) SetDatasourceGitHubClientFactory(factory func(token string) GitHubContentClient) {
	s.newGitHubClient = factory
}

// SetDatasourceAPIClientFactory overrides how per-Source API clients are built.
// Tests use it to inject a fake without a live HTTP endpoint.
func (s *Service) SetDatasourceAPIClientFactory(factory func(cfg APIDatasourceConfig, credential string) APIContentClient) {
	s.newAPIClient = factory
}

// AddGitHubSource registers a GitHub repository as a Source. The access token
// (and optional webhook signing secret) are encrypted and stored only as
// envelope references. The returned Source carries no credential material.
func (s *Service) AddGitHubSource(ctx context.Context, actorID string, input AddGitHubSourceInput) (*DatasourceSource, error) {
	w := wool.Get(ctx).In("AddGitHubSource")

	orgID := strings.TrimSpace(input.OrgID)
	if orgID == "" {
		return nil, w.NewError("org id is required")
	}
	repo := strings.TrimSpace(input.Repo)
	if !validRepo(repo) {
		return nil, w.NewError("repo must be in owner/name form")
	}
	targetCollection := strings.TrimSpace(input.TargetCollection)
	if targetCollection == "" {
		return nil, w.NewError("target collection is required")
	}
	if strings.TrimSpace(input.AccessToken) == "" {
		return nil, w.NewError("access token is required")
	}
	if s.datasourceCipher == nil {
		return nil, w.NewError("datasource secret cipher is not configured")
	}

	source := &DatasourceSource{
		ID:               NewIDString(),
		OrgID:            orgID,
		Provider:         DatasourceProviderGitHub,
		Repo:             repo,
		Paths:            normalizePaths(input.Paths),
		Branch:           strings.TrimSpace(input.Branch),
		TargetCollection: targetCollection,
		Status:           DatasourceStatusActive,
	}

	credentialRef, err := s.datasourceCipher.EncryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), input.AccessToken)
	if err != nil {
		return nil, w.Wrapf(err, "encrypt access token")
	}
	source.CredentialSecretRef = credentialRef

	if secret := strings.TrimSpace(input.WebhookSecret); secret != "" {
		webhookRef, err := s.datasourceCipher.EncryptSecret(ctx, DatasourceWebhookSecretPurpose(source.ID), secret)
		if err != nil {
			return nil, w.Wrapf(err, "encrypt webhook secret")
		}
		source.WebhookSecretRef = webhookRef
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.InsertDatasourceSource(ctx, source)
	}); err != nil {
		return nil, w.Wrapf(err, "persist datasource source")
	}
	s.emit(ctx, actorID, "user", EventDatasourceSourceAdded, "datasource", source.ID, orgID,
		map[string]any{"repo": source.Repo})
	return source, nil
}

// AddSourceInput is the provider-agnostic connect input. Provider selects the
// connector; the matching config fields are read (Repo/Paths/Branch for GitHub,
// API for the generic API provider). Credential and WebhookSecret are plaintext,
// each encrypted through the SecretCipher and persisted only as an envelope
// reference.
type AddSourceInput struct {
	OrgID            string
	Provider         string
	TargetCollection string
	Credential       string
	WebhookSecret    string

	// GitHub provider config.
	Repo   string
	Paths  []string
	Branch string

	// API provider config.
	API *APIDatasourceConfig
}

// AddSource registers a datasource for any provider. It validates the config for
// the selected provider, encrypts the credential (and, where the provider
// supports webhooks, the signing secret), persists the non-secret row, and
// returns it without credential material.
func (s *Service) AddSource(ctx context.Context, actorID string, input AddSourceInput) (*DatasourceSource, error) {
	w := wool.Get(ctx).In("AddSource")

	orgID := strings.TrimSpace(input.OrgID)
	if orgID == "" {
		return nil, w.NewError("org id is required")
	}
	targetCollection := strings.TrimSpace(input.TargetCollection)
	if targetCollection == "" {
		return nil, w.NewError("target collection is required")
	}
	if strings.TrimSpace(input.Credential) == "" {
		return nil, w.NewError("credential is required")
	}
	if s.datasourceCipher == nil {
		return nil, w.NewError("datasource secret cipher is not configured")
	}

	source := &DatasourceSource{
		ID:               NewIDString(),
		OrgID:            orgID,
		Provider:         input.Provider,
		TargetCollection: targetCollection,
		Status:           DatasourceStatusActive,
	}

	switch input.Provider {
	case DatasourceProviderGitHub:
		repo := strings.TrimSpace(input.Repo)
		if !validRepo(repo) {
			return nil, w.NewError("repo must be in owner/name form")
		}
		source.Repo = repo
		source.Paths = normalizePaths(input.Paths)
		source.Branch = strings.TrimSpace(input.Branch)
	case DatasourceProviderAPI:
		// The generic API provider has no webhook receiver yet; refuse a secret
		// nothing would ever verify rather than storing dead credential material.
		if strings.TrimSpace(input.WebhookSecret) != "" {
			return nil, w.NewError("api provider does not support webhooks")
		}
		cfg, err := normalizeAPIConfig(input.API)
		if err != nil {
			return nil, w.Wrap(err)
		}
		source.API = cfg
	default:
		return nil, w.NewError("unknown datasource provider")
	}

	credentialRef, err := s.datasourceCipher.EncryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), input.Credential)
	if err != nil {
		return nil, w.Wrapf(err, "encrypt credential")
	}
	source.CredentialSecretRef = credentialRef

	if secret := strings.TrimSpace(input.WebhookSecret); secret != "" {
		webhookRef, err := s.datasourceCipher.EncryptSecret(ctx, DatasourceWebhookSecretPurpose(source.ID), secret)
		if err != nil {
			return nil, w.Wrapf(err, "encrypt webhook secret")
		}
		source.WebhookSecretRef = webhookRef
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.InsertDatasourceSource(ctx, source)
	}); err != nil {
		return nil, w.Wrapf(err, "persist datasource source")
	}
	s.emit(ctx, actorID, "user", EventDatasourceSourceAdded, "datasource", source.ID, orgID,
		map[string]any{"provider": source.Provider})
	return source, nil
}

// normalizeAPIConfig validates and trims a generic API provider config.
func normalizeAPIConfig(cfg *APIDatasourceConfig) (*APIDatasourceConfig, error) {
	if cfg == nil {
		return nil, errors.New("api config is required")
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("api base url must be an absolute http(s) url")
	}
	out := &APIDatasourceConfig{
		BaseURL:        baseURL,
		ResourcePath:   strings.TrimSpace(cfg.ResourcePath),
		CredentialKind: strings.TrimSpace(cfg.CredentialKind),
	}
	switch out.CredentialKind {
	case APICredentialKindBearer, APICredentialKindBasic:
	case APICredentialKindHeader:
		out.CredentialHeader = strings.TrimSpace(cfg.CredentialHeader)
		if out.CredentialHeader == "" {
			return nil, errors.New("api header credential kind requires a header name")
		}
	default:
		return nil, errors.New("api credential kind must be bearer, basic, or header")
	}
	return out, nil
}

// ListDatasourceSources returns the org's connected Sources.
func (s *Service) ListDatasourceSources(ctx context.Context, orgID string) ([]*DatasourceSource, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, errors.New("org id is required")
	}
	var sources []*DatasourceSource
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		list, err := s.store.ListDatasourceSources(ctx, orgID)
		sources = list
		return err
	}); err != nil {
		return nil, err
	}
	return sources, nil
}

// GetDatasourceSource returns one connected Source in the org, or
// ErrDatasourceSourceNotFound.
func (s *Service) GetDatasourceSource(ctx context.Context, orgID, id string) (*DatasourceSource, error) {
	orgID = strings.TrimSpace(orgID)
	id = strings.TrimSpace(id)
	if orgID == "" || id == "" {
		return nil, errors.New("org id and source id are required")
	}
	var source *DatasourceSource
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		found, err := s.store.GetDatasourceSource(ctx, orgID, id)
		source = found
		return err
	}); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, ErrDatasourceSourceNotFound
	}
	return source, nil
}

// DeleteDatasourceSource removes a Source and its stored credentials.
func (s *Service) DeleteDatasourceSource(ctx context.Context, actorID, orgID, id string) error {
	orgID = strings.TrimSpace(orgID)
	id = strings.TrimSpace(id)
	if orgID == "" || id == "" {
		return errors.New("org id and source id are required")
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.DeleteDatasourceSource(ctx, orgID, id)
	}); err != nil {
		return err
	}
	s.emit(ctx, actorID, "user", EventDatasourceSourceRemoved, "datasource", id, orgID)
	return nil
}

// SyncDatasourceSource enqueues a durable request to pull the Source's current
// contents and returns the request's job id. The pull itself runs off-request in
// a leased worker (RunDatasourceSync), so a large repository cannot block or time
// out the RPC and the walk gets the jobs framework's retry/backoff — long enough
// to outlast a GitHub rate-limit window. The Source must belong to orgID.
func (s *Service) SyncDatasourceSource(ctx context.Context, actorID, orgID, id string) (string, error) {
	w := wool.Get(ctx).In("SyncDatasourceSource")
	source, err := s.GetDatasourceSource(ctx, orgID, id)
	if err != nil {
		return "", err
	}
	if s.datasourceJobs == nil {
		return "", w.NewError("datasource connector is not configured")
	}
	response, err := s.datasourceJobs.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: source.OrgID}},
			Queue:     DatasourceSyncRequestQueue,
			Topic:     datasourceSyncRequestTopic,
			Source:    datasourceSyncRequestSource,
			// A fresh key per request: a sync is an explicit "reconcile now" action,
			// so it must never be dropped as an idempotent replay of an earlier,
			// already-terminal request.
			IdempotencyKey: NewIDString(),
			SchemaVersion:  datasourceSyncRequestSchemaVersion,
			MaxAttempts:    datasourceSyncRequestMaxAttempts,
			Attributes:     map[string]string{attrSourceID: source.ID},
		},
	})
	if err != nil {
		return "", w.Wrapf(err, "enqueue sync request")
	}
	s.emit(ctx, actorID, "user", EventDatasourceSourceSynced, "datasource", source.ID, orgID)
	return response.GetJobId(), nil
}

// RunDatasourceSync performs the actual pull for one Source, dispatched by
// provider. It is invoked by the leased sync worker, never by request traffic.
// Returns the number of ingest deliveries enqueued.
func (s *Service) RunDatasourceSync(ctx context.Context, sourceID string) (int, error) {
	w := wool.Get(ctx).In("RunDatasourceSync")
	if s.datasourceCipher == nil || s.datasourceJobs == nil {
		return 0, w.NewError("datasource connector is not configured")
	}
	source, err := s.store.GetDatasourceSourceByID(ctx, sourceID)
	if err != nil {
		return 0, w.Wrapf(err, "load source")
	}
	if source == nil {
		return 0, ErrDatasourceSourceNotFound
	}

	switch source.Provider {
	case DatasourceProviderGitHub:
		return s.runGitHubSync(ctx, source)
	case DatasourceProviderAPI:
		return s.runAPISync(ctx, source)
	default:
		return 0, w.NewError("unknown datasource provider")
	}
}

// runGitHubSync resolves the ref to a commit, lists the in-scope files, and
// enqueues one ingest delivery per file. Files too large for the contents API
// are skipped rather than aborting the walk.
func (s *Service) runGitHubSync(ctx context.Context, source *DatasourceSource) (int, error) {
	w := wool.Get(ctx).In("runGitHubSync")
	if s.newGitHubClient == nil {
		return 0, w.NewError("datasource connector is not configured")
	}

	token, err := s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), source.CredentialSecretRef)
	if err != nil {
		return 0, w.Wrapf(err, "decrypt access token")
	}
	client := s.newGitHubClient(token)

	ref := source.Branch
	if ref == "" {
		ref, err = client.DefaultBranch(ctx, source.Repo)
		if err != nil {
			return 0, w.Wrapf(err, "resolve default branch")
		}
	}
	// The commit sha is monotonic across pushes even when file content reverts,
	// so it keys delivery idempotency: an A→B→A revert lands under three distinct
	// commits and is re-delivered, while a re-sync at the same commit dedupes.
	commit, err := client.ResolveCommit(ctx, source.Repo, ref)
	if err != nil {
		return 0, w.Wrapf(err, "resolve commit")
	}

	files, err := client.ListFiles(ctx, source.Repo, ref, source.Paths)
	if err != nil {
		return 0, w.Wrapf(err, "list repository files")
	}

	enqueued := 0
	for _, file := range files {
		content, err := client.GetFileContent(ctx, source.Repo, ref, file.Path)
		if err != nil {
			// A missing or too-large file is skipped, not fatal: a single oversized
			// asset must never abort ingestion of the rest of the repository.
			if errors.Is(err, github.ErrNotFound) || errors.Is(err, github.ErrFileTooLarge) {
				w.Warn("skipping file", wool.Field("path", file.Path), wool.ErrField(err))
				continue
			}
			return enqueued, w.Wrapf(err, "fetch %s", file.Path)
		}
		if len(content) > maxIngestPayload {
			w.Warn("skipping oversized file", wool.Field("path", file.Path), wool.Field("bytes", len(content)))
			continue
		}
		if err := s.enqueueIngest(ctx, source, file.Path, ref, commit, file.SHA, changeTypeAdded, content); err != nil {
			return enqueued, w.Wrapf(err, "enqueue %s", file.Path)
		}
		enqueued++
	}

	if err := s.store.WithOrgTx(ctx, source.OrgID, func(ctx context.Context) error {
		return s.store.SetDatasourceSourceSynced(ctx, source.OrgID, source.ID, time.Now().UTC())
	}); err != nil {
		return enqueued, w.Wrapf(err, "record sync time")
	}
	return enqueued, nil
}

// runAPISync fetches the API source's configured resource and enqueues its body
// as a single ingest delivery. Re-sync with unchanged content dedupes on the
// content hash; changed content is re-delivered.
func (s *Service) runAPISync(ctx context.Context, source *DatasourceSource) (int, error) {
	w := wool.Get(ctx).In("runAPISync")
	if s.newAPIClient == nil {
		return 0, w.NewError("datasource connector is not configured")
	}
	if source.API == nil {
		return 0, w.NewError("api source has no config")
	}

	credential, err := s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), source.CredentialSecretRef)
	if err != nil {
		return 0, w.Wrapf(err, "decrypt credential")
	}

	result, err := s.newAPIClient(*source.API, credential).Fetch(ctx)
	if err != nil {
		return 0, w.Wrapf(err, "fetch api resource")
	}
	if len(result.Body) > maxIngestPayload {
		return 0, w.NewError("api response exceeds the ingest payload limit")
	}
	if err := s.enqueueAPIIngest(ctx, source, result); err != nil {
		return 0, w.Wrapf(err, "enqueue api delivery")
	}

	if err := s.store.WithOrgTx(ctx, source.OrgID, func(ctx context.Context) error {
		return s.store.SetDatasourceSourceSynced(ctx, source.OrgID, source.ID, time.Now().UTC())
	}); err != nil {
		return 1, w.Wrapf(err, "record sync time")
	}
	return 1, nil
}

func (s *Service) enqueueAPIIngest(ctx context.Context, source *DatasourceSource, result *apisource.Result) error {
	contentType := result.ContentType
	if contentType == "" {
		contentType = datasourceIngestContentType
	}
	digest := sha256.Sum256(result.Body)
	contentSHA := hex.EncodeToString(digest[:])
	_, err := s.datasourceJobs.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			Queue:          datasourceIngestQueue,
			Topic:          datasourceAPISyncTopic,
			Source:         datasourceAPISyncSource,
			IdempotencyKey: "datasource-api-sync/" + source.ID + "/" + contentSHA,
			SchemaVersion:  datasourceIngestSchemaVersion,
			Payload:        result.Body,
			ContentType:    contentType,
			MaxAttempts:    datasourceIngestMaxAttempts,
			Attributes: map[string]string{
				attrSourceID:         source.ID,
				attrOrgID:            source.OrgID,
				attrTargetCollection: source.TargetCollection,
				attrAPIURL:           apiResourceURL(source.API),
				attrAPIContentSHA:    contentSHA,
				attrChangeType:       changeTypeAdded,
			},
		},
	})
	return err
}

func apiResourceURL(cfg *APIDatasourceConfig) string {
	if cfg.ResourcePath == "" {
		return cfg.BaseURL
	}
	return strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.TrimLeft(cfg.ResourcePath, "/")
}

// NewDatasourceSyncJobHandler adapts RunDatasourceSync to the generic leased
// worker. Malformed routing is a permanent failure (safe to retain); a pull
// failure stays retryable so the framework retries past a transient GitHub
// outage or rate-limit window. A source deleted between request and lease is a
// no-op success.
func (s *Service) NewDatasourceSyncJobHandler() jobs.Handler {
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		if envelope.GetQueue() != DatasourceSyncRequestQueue || envelope.GetTopic() != datasourceSyncRequestTopic {
			return jobs.NewProcessingError("datasource.invalid_job", "unexpected datasource sync job routing", false)
		}
		sourceID := envelope.GetAttributes()[attrSourceID]
		if sourceID == "" {
			return jobs.NewProcessingError("datasource.invalid_job", "datasource sync job has no source id", false)
		}
		if _, err := s.RunDatasourceSync(ctx, sourceID); err != nil && !errors.Is(err, ErrDatasourceSourceNotFound) {
			return err
		}
		return nil
	}
}

// SigningSecret resolves the plaintext HMAC signing secret for one Source. It is
// the Vault-transit-backed replacement for the receiver's StaticSecretResolver
// (pkg/datasource, issue #275): the unauthenticated webhook path has no tenant
// context, so the lookup runs through the control-plane by-id read. An unknown
// or webhook-unconfigured Source returns ErrDatasourceSourceNotFound.
func (s *Service) SigningSecret(ctx context.Context, sourceID string) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return "", ErrDatasourceSourceNotFound
	}
	if s.datasourceCipher == nil {
		return "", errors.New("datasource secret cipher is not configured")
	}
	source, err := s.store.GetDatasourceSourceByID(ctx, sourceID)
	if err != nil {
		return "", err
	}
	if source == nil || !source.WebhookConfigured() {
		return "", ErrDatasourceSourceNotFound
	}
	return s.datasourceCipher.DecryptSecret(ctx, DatasourceWebhookSecretPurpose(sourceID), source.WebhookSecretRef)
}

func (s *Service) enqueueIngest(ctx context.Context, source *DatasourceSource, path, ref, commit, sha, changeType string, content []byte) error {
	_, err := s.datasourceJobs.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			// Global scope matches the inbound webhook producer (pkg/datasource) on
			// the shared "datasource" queue, so the documents consumer sees one
			// consistent scope across both producers; the owning org travels in the
			// attributes for tenant attribution.
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			Queue:          datasourceIngestQueue,
			Topic:          datasourceSyncTopic,
			Source:         datasourceSyncSource,
			IdempotencyKey: ingestIdempotencyKey(source.ID, commit, path),
			SchemaVersion:  datasourceIngestSchemaVersion,
			Payload:        content,
			ContentType:    datasourceIngestContentType,
			MaxAttempts:    datasourceIngestMaxAttempts,
			Attributes: map[string]string{
				attrSourceID:         source.ID,
				attrOrgID:            source.OrgID,
				attrTargetCollection: source.TargetCollection,
				attrRepo:             source.Repo,
				attrPath:             path,
				attrRef:              ref,
				attrSHA:              sha,
				attrCommit:           commit,
				attrChangeType:       changeType,
			},
		},
	})
	// A re-sync at the same commit resolves to a durable duplicate (same key,
	// same fingerprint) and returns no error; only a genuine fingerprint conflict
	// on a reused key surfaces, which the caller propagates.
	return err
}

// ingestIdempotencyKey is deterministic in (source, commit, path) and bounded,
// so an unbounded repo path cannot overflow the inbox idempotency column, a
// re-sync at an unchanged commit dedupes to the stored delivery, and a revert to
// earlier content under a new commit is delivered rather than dropped.
func ingestIdempotencyKey(sourceID, commit, path string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + commit + "\x00" + path))
	return "datasource-sync/" + hex.EncodeToString(digest[:])
}

// DatasourceSyncRetryDelay backs off a failed sync pull far enough that a
// retry outlasts GitHub's hourly rate-limit window before giving up.
func DatasourceSyncRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
	}
	if attempt == 0 {
		return schedule[0]
	}
	index := int(attempt - 1)
	if index >= len(schedule) {
		index = len(schedule) - 1
	}
	return schedule[index]
}

func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return false
	}
	return owner != "" && name != "" && !strings.Contains(name, "/")
}

// normalizePaths trims, de-slashes, drops empties, and de-duplicates the path
// prefixes so the stored set and the GitHub tree filter agree.
func normalizePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
