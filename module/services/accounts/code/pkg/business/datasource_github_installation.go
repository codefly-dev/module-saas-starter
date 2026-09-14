package business

import (
	"context"
	"errors"
	"strings"

	"github.com/codefly-dev/core/wool"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/githubconnector"
	"accounts/pkg/jobs"
)

// The App-level lifecycle seam. These mirror the constants the receiver stamps
// (pkg/datasource, issue #691); that package imports this one, so they cannot
// be shared without an import cycle.
const (
	DatasourceInstallationQueue = "datasource.installations"
	datasourceInstallationTopic = "datasource.github.installation"
	attrInstallationID          = "datasource.installation_id"
)

// ErrGitHubAppWebhookUnconfigured reports that this deployment registered no
// App webhook secret, so no App-level delivery can be verified.
var ErrGitHubAppWebhookUnconfigured = errors.New("business: no github app webhook secret configured")

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
// when App access returns.
var datasourceInstallationReasons = []string{
	DatasourceReasonInstallationRepositoryUnavailable,
	DatasourceReasonInstallationSuspended,
}

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
func (s *Service) ReconcileGitHubInstallation(ctx context.Context, installationID string) error {
	w := wool.Get(ctx).In("ReconcileGitHubInstallation")
	if s.githubConnector == nil || !s.GitHubAppConfigured() {
		return jobs.NewProcessingError("datasource.github_app_unconfigured",
			"This deployment has no GitHub App registration to verify an installation against. Restore the App registration. This job will not retry.", false)
	}

	var sources []*DatasourceSource
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		sources, err = s.store.ListDatasourceSourcesByGitHubInstallation(ctx, installationID)
		return err
	}); err != nil {
		return w.Wrapf(err, "list installation sources")
	}

	registration := githubconnector.AppCredential{AppID: s.githubAppID, PrivateKeyPEM: s.githubAppKeyPEM}
	// One installation usually covers several sources, so its suspension state
	// is read once per covering installation rather than once per source.
	suspended := map[string]bool{}
	for _, source := range sources {
		reason, err := s.githubInstallationAccess(ctx, registration, source, suspended)
		if err != nil {
			return err
		}
		if err := s.applyGitHubInstallationAccess(ctx, source, reason); err != nil {
			return err
		}
	}
	return nil
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
		return "", githubInstallationTokenError(err)
	}

	isSuspended, known := suspended[covering]
	if !known {
		installation, err := s.githubConnector.GetInstallation(ctx, registration, covering)
		if err != nil {
			if githubconnector.IsNotFound(err) {
				return DatasourceReasonInstallationRepositoryUnavailable, nil
			}
			return "", githubInstallationTokenError(err)
		}
		isSuspended = installation.SuspendedAt != nil
		suspended[covering] = isSuspended
	}
	if isSuspended {
		return DatasourceReasonInstallationSuspended, nil
	}
	return "", nil
}

// applyGitHubInstallationAccess parks or revives one source, and leaves every
// other status alone. Parking is confined to an active source so an operator's
// pause is not overwritten and a source the compiler degraded keeps its own
// reason; reviving is confined to the reasons this path writes, so restored
// access cannot clear an unrelated fault. A source already in the state it
// should be in is not rewritten, so a redelivery churns no rows.
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
	if source.Status != DatasourceStatusActive {
		return nil
	}
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.MarkDatasourceSourceDegraded(ctx, source.ID, reason)
	}); err != nil {
		return w.Wrapf(err, "park source")
	}
	return nil
}
