package business

import (
	"accounts/pkg/datasource/github"
	"accounts/pkg/jobs"
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A rejected ciphertext needs new credentials; a service outage must remain retryable.
func datasourceCredentialError(err error) error {
	var httpErr interface{ HTTPStatusCode() int }
	if errors.Is(err, ErrInvalidSecretEnvelope) || (errors.As(err, &httpErr) && httpErr.HTTPStatusCode() == 400) {
		return jobs.NewProcessingError("datasource.credential_unreadable", "Stored GitHub credential cannot be decrypted. Reconnect this source with a new PAT. This sync job will not retry.", false)
	}
	return jobs.NewProcessingError("datasource.credential_service_unavailable", "The credential service could not read the saved GitHub token. Retry shortly; if this persists, reconnect the source.", true)
}

func datasourceProcessingError(err error) error {
	var safe *jobs.ProcessingError
	if errors.As(err, &safe) {
		return safe
	}
	switch {
	case errors.Is(err, github.ErrUnauthorized):
		return jobs.NewProcessingError("datasource.github_unauthorized", "GitHub rejected the PAT (401). Reconnect with a valid token. This sync job will not retry.", false)
	case errors.Is(err, github.ErrRateLimited):
		return jobs.NewProcessingError("datasource.github_rate_limited", "GitHub rate limited the request (403/429). This job may retry.", true)
	case errors.Is(err, github.ErrForbidden):
		return jobs.NewProcessingError("datasource.github_access_or_rate_limit", "GitHub denied access or rate limited the request (403/429). Check repository permissions and organization approval; this job may retry.", true)
	case errors.Is(err, github.ErrNotFound):
		return jobs.NewProcessingError("datasource.github_not_found", "GitHub repository, branch, or content was not found or is inaccessible to this PAT (404). Check the source configuration and token permissions; this job may retry.", true)
	}
	return err
}

func datasourceFailureFields(err error, repo, trigger string) map[string]any {
	fields := map[string]any{"repo": repo, "trigger": trigger, "reason": "Source fetch or dispatch failed. Retry shortly or check service logs.", "code": "datasource.sync_failed", "retryable": true}
	var safe *jobs.ProcessingError
	if errors.As(datasourceProcessingError(err), &safe) {
		fields["reason"], fields["code"], fields["retryable"] = safe.Failure.Message, safe.Failure.Code, safe.Retryable
	} else if st, ok := status.FromError(err); ok && (st.Code() == codes.FailedPrecondition || st.Code() == codes.Unavailable) {
		// Only our fixed GitHub validation messages are eligible, never arbitrary upstream text.
		for _, cause := range []error{github.ErrUnauthorized, github.ErrForbidden, github.ErrNotFound} {
			if st.Message() == status.Convert(githubValidationError(cause)).Message() {
				fields["reason"] = st.Message()
				fields["code"] = "datasource.validation_failed"
				fields["retryable"] = false
			}
		}
	}
	return fields
}
