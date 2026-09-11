package business

import (
	"accounts/pkg/datasource/github"
	"context"
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"time"
)

// Validate with GitHub before persisting credentials or accepting an explicit sync.
// Provider response bodies and tokens never become user-facing error messages.
func (s *Service) validateGitHubSource(ctx context.Context, repo, branch, token string) error {
	if s.newGitHubClient == nil {
		return status.Error(codes.FailedPrecondition, "GitHub connector is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := s.newGitHubClient(strings.TrimSpace(token))
	defaultBranch, err := client.DefaultBranch(ctx, repo)
	if err != nil {
		return githubValidationError(err)
	}
	if strings.TrimSpace(branch) == "" {
		branch = defaultBranch
	}
	_, err = client.ResolveCommit(ctx, repo, strings.TrimSpace(branch))
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
		return status.Error(codes.FailedPrecondition, "GitHub denied access or rate limited the request (403/429). Check repository permissions and organization SSO authorization, or retry later.")
	case errors.Is(err, github.ErrNotFound):
		return status.Error(codes.FailedPrecondition, "GitHub repository or branch was not found or is not accessible to this token (404). Check owner/repository, branch, and Contents read permission.")
	default:
		return status.Error(codes.Unavailable, "Could not validate GitHub repository access. GitHub may be unavailable; retry shortly.")
	}
}
