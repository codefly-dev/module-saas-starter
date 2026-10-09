package business

import (
	"context"
	"errors"

	"accounts/pkg/datasource/apisource"
	"accounts/pkg/jobs"
)

// AddExistingSource stores a source the way AddSource did before its provider
// stopped admitting new sources, so a test can exercise what the host still
// does for a source that already exists: sync it, refresh its credential,
// validate what it was connected with. It is compiled into tests only;
// production has no path around admission.
func (s *Service) AddExistingSource(ctx context.Context, actorID string, input AddSourceInput) (*DatasourceSource, error) {
	return s.addSource(ctx, actorID, input)
}

// DatasourceProcessingErrorForTest exposes how a sync job's failure is typed.
func DatasourceProcessingErrorForTest(err error) *jobs.ProcessingError {
	var out *jobs.ProcessingError
	if errors.As(datasourceProcessingError(err), &out) {
		return out
	}
	return nil
}

// DatasourceCredentialKeyForTest exposes which credential a source spends.
func DatasourceCredentialKeyForTest(source *DatasourceSource) string {
	return datasourceCredentialKey(source)
}

// OAuth test seams retain the real store and cipher, substituting only the provider.
func (s *Service) SetDatasourceClientCredentialsForTest(fn OAuth2RefreshFunc) {
	s.newOAuth2ClientCredentials = fn
}
func (s *Service) ResolveDatasourceTokenForTest(ctx context.Context, source *DatasourceSource) (string, error) {
	return s.resolveOAuth2AccessToken(ctx, source)
}
func (s *Service) SetDatasourceAuthorizationCodeForTest(fn func(context.Context, apisource.OAuth2Config, string, string, string, string) (*apisource.OAuth2Token, error)) {
	s.newOAuth2AuthorizationCode = fn
}

func (s *Service) SetSourceOperationClientForTest(fn func(apisource.Config, string) APIOperationClient) {
	s.newAPIOperationClient = fn
}
