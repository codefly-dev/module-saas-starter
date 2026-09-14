package business

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/githubconnector"
	"accounts/pkg/jobs"
)

// How a GitHub source authenticates. A `pat` source presents a stored
// fine-grained token; an `app` source presents an installation token the host
// mints for each fetch and never stores.
const (
	githubCredentialKindPAT = "pat"
	githubCredentialKindApp = "app"
)

// githubInstallationReadPermissions is the whole authority a source needs to
// read a repository: file contents, plus the metadata every request resolves
// the repository through. Narrowing here is defence in depth, not the boundary
// — repository permission has no path grain, so the host still enforces the
// source's configured path prefixes itself.
var githubInstallationReadPermissions = map[string]string{"contents": "read", "metadata": "read"}

// githubStoredCredential is the plaintext behind a GitHub source's credential
// envelope. An app source stores only the installation it was bound to and the
// revision of that binding — never a token (they last an hour) and never the
// App's signing key, which is deployment custody.
type githubStoredCredential struct {
	Kind           string `json:"kind"`
	AccessToken    string `json:"access_token,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
	Revision       int    `json:"revision,omitempty"`
}

func (c githubStoredCredential) marshal() (string, error) {
	blob, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(blob), nil
}

// parseGitHubStoredCredential decodes a source's credential envelope. Sources
// connected before the App lifecycle stored the PAT as bare text rather than a
// JSON object, so anything that does not decode into this shape is that PAT.
func parseGitHubStoredCredential(plaintext string) githubStoredCredential {
	var cred githubStoredCredential
	if err := json.Unmarshal([]byte(plaintext), &cred); err != nil || cred.Kind == "" {
		return githubStoredCredential{Kind: githubCredentialKindPAT, AccessToken: plaintext}
	}
	return cred
}

// SetGitHubAppRegistration wires the deployment's GitHub App: the id it is
// registered under and the RSA private key its installation tokens are signed
// with. Both are operator-managed deployment configuration, held once here
// rather than copied onto each source, and never leave accounts. An empty
// registration leaves every source on its own stored PAT.
func (s *Service) SetGitHubAppRegistration(appID, privateKeyPEM string) {
	s.githubAppID = strings.TrimSpace(appID)
	s.githubAppKeyPEM = strings.TrimSpace(privateKeyPEM)
}

// GitHubAppConfigured reports whether this deployment can mint installation
// tokens at all.
func (s *Service) GitHubAppConfigured() bool {
	return s.githubAppID != "" && s.githubAppKeyPEM != ""
}

// appCredentialFor composes the deployment registration with one source's
// installation binding and repository. The scope narrows the minted token to
// that single repository, so a token minted for one source is useless against
// another repository the same installation covers.
func (s *Service) appCredentialFor(cred githubStoredCredential, repo string) githubconnector.AppCredential {
	_, name, _ := strings.Cut(repo, "/")
	return githubconnector.AppCredential{
		AppID:          s.githubAppID,
		InstallationID: cred.InstallationID,
		PrivateKeyPEM:  s.githubAppKeyPEM,
		Scope: &githubconnector.InstallationScope{
			Repositories: []string{name},
			Permissions:  githubInstallationReadPermissions,
		},
		Revision: cred.Revision,
	}
}

// githubClientForSource resolves a source's stored credential into an
// authenticated client. Every GitHub read — connect-time validation, the
// recurring reconcile, a webhook-triggered compile, and immutable content
// fetch — goes through here, so token acquisition and refresh are one
// behaviour rather than five.
func (s *Service) githubClientForSource(ctx context.Context, source *DatasourceSource) (GitHubContentClient, error) {
	if s.datasourceCipher == nil || s.newGitHubClient == nil {
		return nil, errors.New("datasource connector is not configured")
	}
	plaintext, err := s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), source.CredentialSecretRef)
	if err != nil {
		return nil, datasourceCredentialError(err)
	}
	token, err := s.githubToken(ctx, parseGitHubStoredCredential(plaintext), source.Repo)
	if err != nil {
		return nil, err
	}
	return s.newGitHubClient(token), nil
}

// githubToken presents a stored credential as the bearer token a fetch uses. A
// PAT is presented as-is; an App binding is exchanged for a short-lived,
// repository-scoped installation token, which the connector caches per
// authority and re-mints before it lapses.
//
// An App-backed source never silently falls back to a PAT: once access is
// denied, revoked or suspended, the source fails with an actionable error
// until an operator repairs the installation.
func (s *Service) githubToken(ctx context.Context, cred githubStoredCredential, repo string) (string, error) {
	if cred.Kind != githubCredentialKindApp {
		return cred.AccessToken, nil
	}
	if s.githubConnector == nil || !s.GitHubAppConfigured() {
		return "", jobs.NewProcessingError("datasource.github_app_unconfigured",
			"This source authenticates through a GitHub App that this deployment has no registration for. Restore the App registration, or reconnect the source. This sync job will not retry.", false)
	}
	token, err := s.githubConnector.InstallationToken(ctx, s.appCredentialFor(cred, repo))
	if err != nil {
		return "", githubInstallationTokenError(err)
	}
	return token, nil
}

// githubInstallationTokenError classifies a failed mint. A revoked, suspended
// or uninstalled App — and a repository dropped from the installation's
// selection — can never succeed by retrying, so those are terminal; anything
// else is treated as GitHub being briefly unavailable. The provider's own
// message is never surfaced: it can carry request context.
func githubInstallationTokenError(err error) error {
	var apiErr *githubconnector.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
			return jobs.NewProcessingError("datasource.github_app_access_denied",
				"The GitHub App installation for this source no longer grants access to the repository. Reinstall the App or re-select this repository, then sync again. This sync job will not retry.", false)
		case http.StatusUnprocessableEntity:
			return jobs.NewProcessingError("datasource.github_app_scope_denied",
				"The GitHub App installation does not grant the read permissions this source requires. Approve the App's updated permissions, then sync again. This sync job will not retry.", false)
		case http.StatusTooManyRequests:
			return jobs.NewProcessingError("datasource.github_rate_limited",
				"GitHub rate limited the installation-token request. This job may retry.", true)
		}
	}
	return jobs.NewProcessingError("datasource.github_app_token_unavailable",
		"Could not obtain a GitHub App installation token. GitHub may be unavailable; this job may retry.", true)
}

// MigrateGitHubSourceToApp re-points a PAT-backed source at the deployment's
// GitHub App, in place. The source keeps its identity, path scope, boundary,
// grants, delivery cursor and audit history — it is never deleted and
// recreated — and its stored PAT is only overwritten once App access to the
// same repository and branch has actually been proven.
//
// The installation is resolved server-side from the repository the source
// already names, so nothing a caller supplies decides which installation is
// bound to this tenant.
func (s *Service) MigrateGitHubSourceToApp(ctx context.Context, actorID, orgID, id string) (*DatasourceSource, error) {
	w := wool.Get(ctx).In("MigrateGitHubSourceToApp")
	source, err := s.GetDatasourceSource(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if source.Provider != DatasourceProviderGitHub {
		return nil, w.NewError("only a github source authenticates through the github app")
	}
	if s.githubConnector == nil || !s.GitHubAppConfigured() {
		return nil, w.NewError("no github app is registered for this deployment")
	}
	if s.datasourceCipher == nil {
		return nil, w.NewError("datasource secret cipher is not configured")
	}

	owner, _, _ := strings.Cut(source.Repo, "/")
	registration := githubconnector.AppCredential{AppID: s.githubAppID, PrivateKeyPEM: s.githubAppKeyPEM}
	_, repoName, _ := strings.Cut(source.Repo, "/")
	installationID, err := s.githubConnector.FindRepositoryInstallation(ctx, registration, owner, repoName)
	if err != nil {
		return nil, githubInstallationTokenError(err)
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		ref, err := s.store.LockDatasourceSourceCredentialRef(ctx, orgID, id)
		if err != nil {
			return w.Wrapf(err, "lock credential")
		}
		plaintext, err := s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(id), ref)
		if err != nil {
			return datasourceCredentialError(err)
		}
		// The revision advances from whatever is stored now, read under the lock,
		// so a concurrent reconnect cannot leave two bindings sharing a revision
		// and therefore a cached token.
		current := parseGitHubStoredCredential(plaintext)
		replacement := githubStoredCredential{
			Kind:           githubCredentialKindApp,
			InstallationID: installationID,
			Revision:       current.Revision + 1,
		}

		token, err := s.githubToken(ctx, replacement, source.Repo)
		if err != nil {
			return err
		}
		if err := validateGitHubAccess(ctx, s.newGitHubClient(token), source.Repo, source.Branch); err != nil {
			return err
		}

		blob, err := replacement.marshal()
		if err != nil {
			return w.Wrapf(err, "encode app credential")
		}
		encrypted, err := s.datasourceCipher.EncryptSecret(ctx, DatasourceConnectorSecretPurpose(id), blob)
		if err != nil {
			return w.Wrapf(err, "encrypt app credential")
		}
		if err := s.store.UpdateDatasourceSourceCredential(ctx, orgID, id, encrypted); err != nil {
			return w.Wrapf(err, "persist app credential")
		}
		source.CredentialSecretRef = encrypted
		return s.emitTx(ctx, actorID, "user", EventDatasourceCredentialUpdated, "datasource", id, orgID,
			map[string]any{"repo": source.Repo, "credential_kind": githubCredentialKindApp})
	}); err != nil {
		return nil, err
	}
	return source, nil
}
