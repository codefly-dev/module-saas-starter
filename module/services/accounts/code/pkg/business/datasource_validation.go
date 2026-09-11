package business

import (
	"accounts/pkg/datasource/github"
	"accounts/pkg/jobs"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Check repository access before persisting credentials. Explicit syncs apply
// their transient-failure policy in checkGitHubSyncPreflight.
// Provider response bodies and tokens never become user-facing error messages.
func (s *Service) validateGitHubSource(ctx context.Context, repo, branch, token string) error {
	if s.newGitHubClient == nil {
		return status.Error(codes.FailedPrecondition, "GitHub connector is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := s.newGitHubClient(strings.TrimSpace(token))
	branch = strings.TrimSpace(branch)
	if branch == "" {
		var err error
		branch, err = client.DefaultBranch(ctx, repo)
		if err != nil {
			return githubValidationError(err)
		}
	}
	_, err := client.ResolveCommit(ctx, repo, branch)
	if err != nil {
		return githubValidationError(err)
	}
	return nil
}

func githubValidationError(err error) error {
	switch {
	case errors.Is(err, github.ErrUnauthorized):
		return status.Error(codes.FailedPrecondition, "GitHub rejected the access token (401). Check that the PAT is valid and has not expired or been revoked, then reconnect the source.")
	case errors.Is(err, github.ErrForbidden):
		return status.Error(codes.FailedPrecondition, "GitHub denied access (403). Check repository permissions and organization SSO authorization.")
	case errors.Is(err, github.ErrRateLimited):
		return status.Error(codes.Unavailable, "GitHub rate limited the request (403/429). Retry later.")
	case errors.Is(err, github.ErrNotFound):
		return status.Error(codes.FailedPrecondition, "GitHub repository or branch was not found or is not accessible to this token (404). Check owner/repository, branch, and Contents read permission.")
	default:
		return status.Error(codes.Unavailable, "Could not validate GitHub repository access. GitHub may be unavailable; retry shortly.")
	}
}

// A transient preflight failure must not discard a forced repair. The durable
// worker re-reads credentials and retries validation before taking its snapshot.
func (s *Service) checkGitHubSyncPreflight(ctx context.Context, source *DatasourceSource) error {
	if s.datasourceCipher == nil {
		return status.Error(codes.FailedPrecondition, "datasource secret cipher is not configured")
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := s.datasourceCipher.DecryptSecret(preflightCtx, DatasourceConnectorSecretPurpose(source.ID), source.CredentialSecretRef)
	if err != nil {
		var failure *jobs.ProcessingError
		if errors.As(datasourceCredentialError(err), &failure) && !failure.Retryable {
			return status.Error(codes.FailedPrecondition, "Stored GitHub credential is invalid. Reconnect the source.")
		}
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		// Do not log the provider error: it may contain a response body or token.
		wool.Get(ctx).Warn("GitHub credential preflight unavailable; deferring to durable sync", wool.Field("source", source.ID))
		return nil
	}
	err = s.validateGitHubSource(preflightCtx, source.Repo, source.Branch, token)
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if status.Code(err) == codes.Unavailable {
		wool.Get(ctx).Warn("GitHub access preflight unavailable; deferring to durable sync", wool.Field("source", source.ID), wool.Field("reason", status.Convert(err).Message()))
		return nil
	}
	return err
}
