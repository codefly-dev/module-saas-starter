package business

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/githubconnector"
	"accounts/pkg/jobs"
)

// The App-level lifecycle seam. These mirror the constants the receiver stamps
// (pkg/datasource, issue #691); that package imports this one, so they cannot
// be shared without an import cycle.
const (
	DatasourceInstallationQueue             = "datasource.installations"
	datasourceInstallationTopic             = "datasource.github.installation"
	datasourceInstallationOrderingNamespace = "datasource.installation"
	attrInstallationID                      = "datasource.installation_id"

	// datasourceInstallationRecheckSource marks the jobs this host enqueues for
	// itself, as opposed to the ones a verified delivery produced. It must stay
	// in step with datasource.GitHubAppWebhookSchemaVersion so both producers
	// describe the same envelope to one consumer.
	datasourceInstallationRecheckSource = "datasource.installation.recheck"
	datasourceInstallationSchemaVersion = 1

	// datasourceInstallationPageSize bounds one read of an installation's
	// sources. An installation can cover any number of repositories across any
	// number of tenants, so the reconcile pages through them rather than holding
	// the whole set.
	datasourceInstallationPageSize = 100

	// datasourceInstallationRecheckBatch bounds one sweep.
	datasourceInstallationRecheckBatch = 100

	// datasourceInstallationRecheckInterval is how often one parked installation
	// is re-verified. The sweep ticks far more often than that; the window is
	// what throttles it, through the jobs platform's idempotency key.
	datasourceInstallationRecheckInterval = 30 * time.Minute
)

// Why an App-backed source was parked. These are the exact strings written to
// status_reason and the set the reconciler will lift again, so they are
// matched, not just displayed: editing one strands sources already degraded
// under the old text until an operator resets them.
const (
	DatasourceReasonInstallationRepositoryUnavailable = "The GitHub App installation no longer grants access to this repository. Reinstall the App or re-select this repository to resume syncing."
	DatasourceReasonInstallationSuspended             = "The GitHub App installation for this source is suspended. Unsuspend it in GitHub to resume syncing."
)

// datasourceInstallationReasons bounds what this path may revive: a source the
// change-set compiler parked for a structural fault of its own must stay parked
// when App access returns. It is also what the recheck sweep selects on.
var datasourceInstallationReasons = []string{
	DatasourceReasonInstallationRepositoryUnavailable,
	DatasourceReasonInstallationSuspended,
}

// ErrGitHubAppWebhookUnconfigured reports that this deployment registered no
// App webhook secret, so no App-level delivery can be verified.
var ErrGitHubAppWebhookUnconfigured = errors.New("business: no github app webhook secret configured")

// AppWebhookSecret hands the App-level receiver the registration's webhook
// secret. An unconfigured deployment returns ErrGitHubAppWebhookUnconfigured,
// which the receiver answers exactly like a signature failure.
func (s *Service) AppWebhookSecret(_ context.Context) (string, error) {
	if s.githubAppWebhookSecret == "" {
		return "", ErrGitHubAppWebhookUnconfigured
	}
	return s.githubAppWebhookSecret, nil
}

// GitHubAppWebhookConfigured reports whether this deployment can verify the
// App's own lifecycle deliveries at all, i.e. whether the App-level receiver
// should be mounted.
func (s *Service) GitHubAppWebhookConfigured() bool {
	return s.GitHubAppConfigured() && s.githubAppWebhookSecret != ""
}

// NewDatasourceInstallationJobHandler adapts the installation reconciler to the
// leased worker. The delivery body is not read here: the job carries only which
// installation to re-examine, and the reconciler asks GitHub what is true now.
func (s *Service) NewDatasourceInstallationJobHandler() jobs.Handler {
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		if envelope.GetQueue() != DatasourceInstallationQueue || envelope.GetTopic() != datasourceInstallationTopic {
			return jobs.NewProcessingError("datasource.invalid_job", "unexpected datasource installation job routing", false)
		}
		installationID := envelope.GetAttributes()[attrInstallationID]
		if installationID == "" {
			return jobs.NewProcessingError("datasource.invalid_job", "datasource installation job has no installation id", false)
		}
		return s.ReconcileGitHubInstallation(ctx, installationID)
	}
}

// ReconcileGitHubInstallation brings every source bound to one App installation
// back in line with the access GitHub currently grants. It is driven by an
// App-level delivery but never acts on one: the delivery names an installation,
// and everything else is re-derived, so a replayed suspend cannot re-park a
// source whose installation has since been restored.
//
// Losing access parks a source; it never deletes one. The row keeps its
// boundary, path scope, ingest cursor, grants and audit history, so restoring
// access resumes where the source left off instead of re-ingesting it, and the
// content already ingested stays governed by the tenant's own retention policy
// rather than by a third party's webhook.
//
// One installation can cover sources in several organizations, so a source that
// cannot be resolved is recorded and stepped over rather than returned on:
// failing the whole job at the first bad repository would leave every source
// behind it — including other tenants' — unexamined.
func (s *Service) ReconcileGitHubInstallation(ctx context.Context, installationID string) error {
	w := wool.Get(ctx).In("ReconcileGitHubInstallation")
	if s.githubConnector == nil || !s.GitHubAppConfigured() {
		return jobs.NewProcessingError("datasource.github_app_unconfigured",
			"This deployment has no GitHub App registration to verify an installation against. Restore the App registration. This job will not retry.", false)
	}

	registration := githubconnector.AppCredential{AppID: s.githubAppID, PrivateKeyPEM: s.githubAppKeyPEM}
	// One installation usually covers several sources, so its suspension state
	// is read once per covering installation rather than once per source.
	suspended := map[string]bool{}
	var failure error
	after := ""
	for {
		var page []*DatasourceSource
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			page, err = s.store.ListDatasourceSourcesByGitHubInstallation(ctx, installationID, after, datasourceInstallationPageSize)
			return err
		}); err != nil {
			return w.Wrapf(err, "list installation sources")
		}
		for _, source := range page {
			after = source.ID
			reason, err := s.githubInstallationAccess(ctx, registration, source, suspended)
			if err != nil {
				w.Warn("installation access check failed", wool.Field("source", source.ID), wool.ErrField(err))
				failure = keepRetryable(failure, err)
				continue
			}
			if err := s.applyGitHubInstallationAccess(ctx, source, reason); err != nil {
				failure = keepRetryable(failure, err)
			}
		}
		if len(page) < datasourceInstallationPageSize {
			return failure
		}
	}
}

// RunGitHubInstallationRecheck re-verifies the installations that still hold a
// parked source, and reports how many it enqueued.
//
// Parking takes a source out of the reconcile sweep, which selects only active
// sources, so without this the single route back to active is another App-level
// delivery — and GitHub does not retry a delivery forever. An `unsuspend` that
// arrives while this host is restarting would otherwise park a tenant's source
// permanently, with no audit entry and nothing to explain it. Restoration
// therefore gets a pull-side path of its own, on the same footing as the
// reconcile safety net that exists for lost push deliveries.
func (s *Service) RunGitHubInstallationRecheck(ctx context.Context) (int, error) {
	w := wool.Get(ctx).In("RunGitHubInstallationRecheck")
	if s.datasourceJobs == nil {
		return 0, w.NewError("datasource connector is not configured")
	}
	if !s.GitHubAppConfigured() {
		return 0, nil
	}

	var installations []string
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		found, err := s.store.ListGitHubInstallationsPendingRecheck(ctx, datasourceInstallationReasons, datasourceInstallationRecheckBatch)
		installations = found
		return err
	}); err != nil {
		return 0, w.Wrapf(err, "list installations pending recheck")
	}

	// One job per installation per window. The sweep runs on the retention
	// ticker, far more often than an installation's state changes, so the
	// window — not the tick — sets the re-check rate: the jobs platform resolves
	// a repeated idempotency key to the job already queued.
	window := time.Now().UTC().Truncate(datasourceInstallationRecheckInterval).Unix()
	enqueued := 0
	for _, installation := range installations {
		response, err := s.datasourceJobs.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{
			Job: &jobsv1.NewJob{
				Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
				Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
				Queue:     DatasourceInstallationQueue,
				Topic:     datasourceInstallationTopic,
				Source:    datasourceInstallationRecheckSource,
				Ordering: &jobsv1.JobOrderingKey{
					Namespace:  datasourceInstallationOrderingNamespace,
					Components: []string{installation},
				},
				IdempotencyKey: fmt.Sprintf("%s:%s:%d", datasourceInstallationRecheckSource, installation, window),
				SchemaVersion:  datasourceInstallationSchemaVersion,
				Payload:        datasourceRequestBody(),
				ContentType:    datasourceRequestContentType,
				MaxAttempts:    datasourceDeliveryMaxAttempts,
				Attributes:     map[string]string{attrInstallationID: installation},
			},
		})
		if err != nil {
			// A conflicting key means this window's job is already recorded with
			// different bytes; either way the installation is covered.
			if !errors.Is(err, jobs.ErrIdempotencyConflict) {
				w.Warn("enqueue installation recheck failed", wool.Field("installation", installation), wool.ErrField(err))
			}
			continue
		}
		if response.GetDisposition() == jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED {
			enqueued++
		}
	}
	return enqueued, nil
}

// githubInstallationAccess reports why a source can no longer read its
// repository, or "" when access is intact.
//
// The question is put to GitHub per source and by repository — never by the
// installation id the delivery carried. A source rebound to a different
// installation since its routing column was stamped is then answered for the
// installation that actually covers it, so a stale column costs a redundant
// check rather than a wrong revocation.
func (s *Service) githubInstallationAccess(ctx context.Context, registration githubconnector.AppCredential, source *DatasourceSource, suspended map[string]bool) (string, error) {
	owner, repo, _ := strings.Cut(source.Repo, "/")
	covering, err := s.githubConnector.FindRepositoryInstallation(ctx, registration, owner, repo)
	if err != nil {
		if githubconnector.IsNotFound(err) {
			return DatasourceReasonInstallationRepositoryUnavailable, nil
		}
		return "", githubInstallationStateError(err)
	}

	isSuspended, known := suspended[covering]
	if !known {
		installation, err := s.githubConnector.GetInstallation(ctx, registration, covering)
		if err != nil {
			if githubconnector.IsNotFound(err) {
				return DatasourceReasonInstallationRepositoryUnavailable, nil
			}
			return "", githubInstallationStateError(err)
		}
		isSuspended = installation.SuspendedAt != nil
		suspended[covering] = isSuspended
	}
	if isSuspended {
		return DatasourceReasonInstallationSuspended, nil
	}
	return "", nil
}

// githubInstallationStateError classifies a failure to READ installation state,
// which is a different question from failing to mint a token and must not reuse
// githubInstallationTokenError.
//
// Minting answers "may this source still read that repository", so a 403 there
// is a real denial and terminal. Here the host authenticates as the App itself
// to ask what GitHub currently reports, and an absent installation already
// answered 404 further up. A 403 on this path is GitHub declining to serve the
// request — overwhelmingly a primary or secondary rate limit, which this path
// invites because it issues a request per source. Treating that as terminal
// dead-letters the job and silently drops the revocation the endpoint exists to
// deliver promptly, so everything except a rejected credential stays retryable.
func githubInstallationStateError(err error) error {
	var apiErr *githubconnector.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		return jobs.NewProcessingError("datasource.github_app_credential_rejected",
			"GitHub rejected this deployment's GitHub App credential. Check the registered App id and signing key. This job will not retry.", false)
	}
	return jobs.NewProcessingError("datasource.github_app_state_unavailable",
		"Could not read the GitHub App installation's current state. GitHub may be unavailable or rate limiting; this job may retry.", true)
}

// keepRetryable prefers a retryable failure over a terminal one, so a job that
// hit both still comes back and finishes the work the transient error stopped.
func keepRetryable(existing, next error) error {
	if existing == nil {
		return next
	}
	var held *jobs.ProcessingError
	if errors.As(existing, &held) && held.Retryable {
		return existing
	}
	var candidate *jobs.ProcessingError
	if errors.As(next, &candidate) && candidate.Retryable {
		return next
	}
	return existing
}

// applyGitHubInstallationAccess parks or revives one source, and leaves every
// other status alone. Both writes carry their own predicate — active for a
// park, degraded-for-one-of-our-reasons for a revival — because the status read
// here comes from a page listed before any GitHub call, and this path is
// ordered per installation, not per source, so nothing stops an operator pause
// or a compiler degrade landing in between. The status checks below are only a
// fast path that saves a statement per unchanged source; the predicates in the
// UPDATEs are what actually decide.
func (s *Service) applyGitHubInstallationAccess(ctx context.Context, source *DatasourceSource, reason string) error {
	w := wool.Get(ctx).In("applyGitHubInstallationAccess")
	if reason == "" {
		if source.Status != DatasourceStatusDegraded {
			return nil
		}
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			return s.store.ClearDatasourceSourceInstallationDegraded(ctx, source.ID, datasourceInstallationReasons)
		}); err != nil {
			return w.Wrapf(err, "restore source")
		}
		return nil
	}
	if source.Status == DatasourceStatusDegraded && source.StatusReason == reason {
		return nil
	}
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.MarkDatasourceSourceInstallationDegraded(ctx, source.ID, reason)
	}); err != nil {
		return w.Wrapf(err, "park source")
	}
	return nil
}
