package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/datasource/github"
	"accounts/pkg/githubconnector"
)

// githubAppSetupTTL bounds how long an install round-trip may take: long enough
// for a tenant to install the App and pick repositories, short enough that a
// state left in browser history or a referrer log is no longer redeemable.
const githubAppSetupTTL = 15 * time.Minute

// githubAppInstallBaseURL is where an App's public install page lives. GitHub
// Enterprise serves it from the appliance host instead; a deployment that needs
// that configures it when it configures the App.
const githubAppInstallBaseURL = "https://github.com"

// githubAppSetupProbeTimeout bounds the GitHub round trips onboarding makes,
// matching the budget connect-time validation and migration already use. Without
// it a hung GitHub holds the request for the server's whole timeout.
const githubAppSetupProbeTimeout = 10 * time.Second

// githubAppMetadataPermissions is the whole authority needed to enumerate what
// an installation grants. Listing repositories must never require the contents
// read a fetch does.
var githubAppMetadataPermissions = map[string]string{"metadata": "read"}

// githubInstallationUnattributableMessage is the single answer to every failure
// to attribute an installation to the caller: a rejected authorization code, a
// code belonging to someone else, and an installation the authorizing user
// cannot reach are all reported identically, so the endpoint never confirms
// which installation ids exist.
const githubInstallationUnattributableMessage = "That GitHub App installation could not be attributed to you. Install the App from the GitHub account or organization you administer and approve the authorization request, then connect again."

// githubInstallationAdministrationUnreadableMessage answers the one failure that
// is not a denial: GitHub would not say whether the caller administers the
// account, so the host refuses rather than assuming either way. Reading it
// requires the App's organization `members: read` permission, which is operator
// configuration — naming it is what makes the refusal actionable.
const githubInstallationAdministrationUnreadableMessage = "This deployment's GitHub App cannot read organization membership, so it cannot confirm that you administer that installation. Grant the App the organization \"Members: Read\" permission and approve it for the organization, then connect again."

// ErrGitHubAppSetupRejected is the single answer to every failed redemption:
// unknown, expired, already consumed, or begun by a different user. One error
// for all of them keeps the endpoint from reporting which states exist.
var ErrGitHubAppSetupRejected = errors.New("github app setup state was rejected")

// GitHubAppSetup is one in-flight App installation round-trip, bound to the
// organization it was begun for and to the user who began it. Only the state's
// digest is persisted.
type GitHubAppSetup struct {
	ID          string
	OrgID       string
	InitiatedBy string
	StateHash   string
	ExpiresAt   time.Time
}

// GitHubAppSetupHandle is what a client needs to send a browser to GitHub and
// come back: where to go, the one-time state that ties the return to this
// organization and user, and when that state lapses.
type GitHubAppSetupHandle struct {
	InstallURL string
	State      string
	ExpiresAt  time.Time
}

// GitHubAppRepository is one repository a verified installation grants.
type GitHubAppRepository struct {
	Repo             string
	DefaultBranch    string
	AlreadyConnected bool
}

// GitHubAppInstallation is a verified installation and what it grants this
// organization.
type GitHubAppInstallation struct {
	InstallationID string
	Repositories   []GitHubAppRepository
}

// newGitHubAppSetupState mints the one-time state. 32 bytes of crypto/rand is
// far beyond guessing, and only its SHA-256 is stored, so a database read never
// yields a redeemable state.
func newGitHubAppSetupState() (plaintext, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, hashGitHubAppSetupState(plaintext), nil
}

func hashGitHubAppSetupState(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}

// githubAppOnboardingReady reports whether this deployment can drive tenant App
// onboarding at all: it needs the registration to mint with, the slug to build
// an install link from, and the App's OAuth client to identify the person who
// comes back from the install.
//
// The OAuth client is not optional. Without it the host can prove an
// installation exists but not that the caller controls it, and an installation
// id is a browser-supplied integer — so onboarding stays off rather than
// binding installations on trust. Sources can still be connected with a
// repository-scoped fine-grained PAT.
func (s *Service) githubAppOnboardingReady() bool {
	return s.GitHubAppConfigured() && s.githubAppSlug != "" && s.githubConnector != nil &&
		s.githubAppClientID != "" && s.githubAppClientSecret != ""
}

// BeginGitHubAppSetup mints a one-time setup state bound to this organization
// and to the calling user, and returns the URL that installs the deployment's
// App on repositories the tenant selects. Nothing about the App's credentials
// leaves accounts: the link carries only the App's public slug and the state.
func (s *Service) BeginGitHubAppSetup(ctx context.Context, actorID, orgID string) (*GitHubAppSetupHandle, error) {
	if !s.githubAppOnboardingReady() {
		return nil, status.Error(codes.FailedPrecondition,
			"This deployment has no GitHub App configured, so a source cannot be connected through one. Connect with a repository-scoped fine-grained PAT, or ask an operator to register the App.")
	}

	state, stateHash, err := newGitHubAppSetupState()
	if err != nil {
		return nil, status.Error(codes.Internal, "Could not start GitHub App setup. Retry shortly.")
	}
	setup := &GitHubAppSetup{
		ID:          NewIDString(),
		OrgID:       orgID,
		InitiatedBy: actorID,
		StateHash:   stateHash,
		ExpiresAt:   time.Now().UTC().Add(githubAppSetupTTL),
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.store.InsertGitHubAppSetup(ctx, setup); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceGitHubAppSetupStarted, "organization", orgID, orgID)
	}); err != nil {
		return nil, err
	}

	return &GitHubAppSetupHandle{
		InstallURL: fmt.Sprintf("%s/apps/%s/installations/new?state=%s",
			githubAppInstallBaseURL, url.PathEscape(s.githubAppSlug), url.QueryEscape(state)),
		State:     state,
		ExpiresAt: setup.ExpiresAt,
	}, nil
}

// CompleteGitHubAppSetup redeems the state exactly once and only then decides
// whether the installation it came back with may be claimed.
//
// The ordering is deliberate. The state is burned first, in its own
// transaction, so a replayed redirect is refused before any work is done and a
// failed verification cannot be retried against the same state.
//
// Two separate things then have to be true, and neither implies the other.
// Asking GitHub as the App whether the installation exists proves only that
// *some* tenant installed it — every installation of this App answers that, so
// on its own it would let any organization claim any other organization's
// installation by naming its id, which is a small integer arriving from a
// browser. So the authorization is the user-to-server exchange: the code GitHub
// appended to the redirect is traded for a token acting as the person holding
// it, and that person must administer the account the installation belongs to —
// not merely reach it, which every member of that organization does. Only then
// is it claimed, where the table's primary key refuses one another tenant
// already holds.
func (s *Service) CompleteGitHubAppSetup(ctx context.Context, actorID, orgID, state, installationID, code string) (*GitHubAppInstallation, error) {
	if !s.githubAppOnboardingReady() {
		return nil, status.Error(codes.FailedPrecondition,
			"This deployment has no GitHub App configured for tenant onboarding, so there is no installation to complete.")
	}
	if strings.TrimSpace(code) == "" {
		return nil, status.Error(codes.InvalidArgument,
			"This GitHub App setup return carried no authorization code, so the installation cannot be attributed to you. The App must request user authorization during installation.")
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.ConsumeGitHubAppSetup(ctx, orgID, hashGitHubAppSetupState(state), actorID, time.Now().UTC())
	}); err != nil {
		if errors.Is(err, ErrGitHubAppSetupRejected) {
			return nil, status.Error(codes.PermissionDenied,
				"This GitHub App setup link is no longer valid. Start the connection again from your organization.")
		}
		return nil, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, githubAppSetupProbeTimeout)
	defer cancel()

	registration := githubconnector.AppCredential{AppID: s.githubAppID, PrivateKeyPEM: s.githubAppKeyPEM}
	installation, err := s.githubConnector.GetInstallation(probeCtx, registration, installationID)
	if err != nil {
		return nil, githubInstallationTokenError(err)
	}
	if installation.SuspendedAt != nil {
		return nil, status.Error(codes.FailedPrecondition,
			"That GitHub App installation is suspended, so it grants no access. Unsuspend it on GitHub, then connect again.")
	}
	if err := s.verifyInstallationAdministeredByCaller(probeCtx, installation, code); err != nil {
		return nil, err
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		claimed, err := s.store.ClaimGitHubAppInstallation(ctx, installation.ID, orgID, actorID)
		if err != nil {
			return err
		}
		if !claimed {
			// Held by a different organization. Reported without naming the
			// holder: which tenant installed an App is not this caller's to learn.
			return status.Error(codes.PermissionDenied,
				"That GitHub App installation is already connected to a different organization.")
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceGitHubAppSetupCompleted, "organization", orgID, orgID,
			map[string]any{"installation_id": installation.ID})
	}); err != nil {
		return nil, err
	}

	repositories, err := s.listGitHubAppRepositories(ctx, orgID, installation.ID)
	if err != nil {
		return nil, err
	}
	return &GitHubAppInstallation{InstallationID: installation.ID, Repositories: repositories}, nil
}

// verifyInstallationAdministeredByCaller proves the person who came back from
// the install administers the account the installation belongs to, by trading
// GitHub's authorization code for a user-to-server token and asking GitHub who
// that user is to that account.
//
// This is the whole authorization of the claim. Everything else in the flow —
// the one-time state, the app-authenticated lookup — establishes that a request
// is fresh and that an installation exists; none of it establishes that this
// caller is entitled to the installation, because the id is an enumerable
// integer supplied by the browser.
//
// Reachability is deliberately not the test. An installation is reachable by
// every member of the organization it is installed on, so authorizing on reach
// would let any member claim their organization's installation for a tenant they
// control — and then connect every repository it grants, including ones they
// cannot read themselves, since the installation token is not their token. Only
// an administrator may install or reconfigure the App, so administration is the
// property that matches the authority being claimed.
//
// Every refusal answers the same way: a rejected code, a code for a different
// person, and an account that person does not administer are indistinguishable
// to the caller, so the endpoint does not become an oracle for which
// installations exist. The one exception is GitHub refusing to answer at all,
// which is an operator misconfiguration rather than a denial and says so.
func (s *Service) verifyInstallationAdministeredByCaller(ctx context.Context, installation githubconnector.AppInstallation, code string) error {
	userToken, err := s.githubConnector.ExchangeUserCode(ctx, s.githubAppClientID, s.githubAppClientSecret, code)
	if err != nil {
		if errors.Is(err, githubconnector.ErrUserCodeRejected) {
			return status.Error(codes.PermissionDenied, githubInstallationUnattributableMessage)
		}
		return githubInstallationTokenError(err)
	}
	administers, err := s.githubConnector.UserAdministersAccount(ctx, userToken, installation.AccountLogin, installation.AccountType)
	if err != nil {
		if errors.Is(err, githubconnector.ErrAccountAdministrationUnreadable) {
			return status.Error(codes.FailedPrecondition, githubInstallationAdministrationUnreadableMessage)
		}
		return githubInstallationTokenError(err)
	}
	if !administers {
		return status.Error(codes.PermissionDenied, githubInstallationUnattributableMessage)
	}
	return nil
}

// listGitHubAppRepositories enumerates what the installation grants, marking the
// repositories this organization already connects so a client does not offer the
// same source twice. The token minted here carries metadata read only — listing
// must not require the contents read a fetch does.
func (s *Service) listGitHubAppRepositories(ctx context.Context, orgID, installationID string) ([]GitHubAppRepository, error) {
	token, err := s.githubConnector.InstallationToken(ctx, githubconnector.AppCredential{
		AppID:          s.githubAppID,
		InstallationID: installationID,
		PrivateKeyPEM:  s.githubAppKeyPEM,
		Scope:          &githubconnector.InstallationScope{Permissions: githubAppMetadataPermissions},
	})
	if err != nil {
		return nil, githubInstallationTokenError(err)
	}
	granted, err := s.githubConnector.ListInstallationRepositories(ctx, token)
	if err != nil {
		return nil, githubInstallationTokenError(err)
	}

	connected := map[string]bool{}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sources, err := s.store.ListDatasourceSources(ctx, orgID)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if source.Provider == DatasourceProviderGitHub {
				connected[strings.ToLower(source.Repo)] = true
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	repositories := make([]GitHubAppRepository, 0, len(granted))
	for _, repo := range granted {
		repositories = append(repositories, GitHubAppRepository{
			Repo:             repo.FullName,
			DefaultBranch:    repo.DefaultBranch,
			AlreadyConnected: connected[strings.ToLower(repo.FullName)],
		})
	}
	return repositories, nil
}

// githubConnectCredential is how a new GitHub source will authenticate, as
// resolved and proven at connect time.
type githubConnectCredential struct {
	// Kind is githubCredentialKindPAT, githubCredentialKindApp or
	// githubCredentialKindPublic.
	Kind string
	// Plaintext is what to seal into the source's credential envelope. Empty for
	// a public source, which stores no envelope at all.
	Plaintext string
	// InstallationID is the App installation backing an App connect, empty
	// otherwise. The caller stamps it onto the source as the routing index an
	// App-level delivery resolves it through: the envelope is encrypted and
	// cannot be selected on, so a source that is App-backed from birth would
	// otherwise be invisible to installation events.
	InstallationID string
}

// resolveGitHubConnectCredential proves a new source can read its repository and
// decides how it authenticates.
//
// A supplied token is a repository-scoped fine-grained PAT and is stored as-is.
// With none, two credential-less paths are tried in order:
//
//  1. The App, when the deployment has one and an installation this
//     organization claimed covers the repository. It is preferred even for a
//     public repository: an installation token carries the installation's rate
//     limit rather than the unauthenticated one every tenant behind this host's
//     IP address shares. The installation is resolved from the repository
//     server-side rather than taken from the caller — so no client decides which
//     installation backs a tenant's source.
//  2. No credential at all, when GitHub itself says the repository is public to
//     a request that carries none (resolvePublicGitHubRepository).
//
// When neither applies the connect is refused with the reason the App path gave,
// or, with no App path, with a request for a PAT — a private or missing
// repository is never connected on a guess.
func (s *Service) resolveGitHubConnectCredential(ctx context.Context, orgID, repo, branch, accessToken string) (githubConnectCredential, error) {
	if token := strings.TrimSpace(accessToken); token != "" {
		if err := s.validateGitHubSource(ctx, repo, branch, token); err != nil {
			return githubConnectCredential{}, err
		}
		return githubConnectCredential{Kind: githubCredentialKindPAT, Plaintext: token}, nil
	}

	appRefusal := status.Error(codes.FailedPrecondition,
		"No access token was supplied and that repository is not public. Supply a repository-scoped fine-grained PAT, or ask an operator to register the GitHub App.")
	if s.GitHubAppConfigured() && s.githubConnector != nil {
		credential, err := s.resolveGitHubAppConnect(ctx, orgID, repo, branch)
		var declined *githubAppDeclined
		if !errors.As(err, &declined) {
			return credential, err
		}
		appRefusal = declined.reason
	}

	public, err := s.resolvePublicGitHubRepository(ctx, repo, branch)
	if err != nil {
		return githubConnectCredential{}, err
	}
	if !public {
		return githubConnectCredential{}, appRefusal
	}
	return githubConnectCredential{Kind: githubCredentialKindPublic}, nil
}

// githubAppDeclined is resolveGitHubAppConnect's answer when the App does not
// cover the repository for this organization. It is not a failure of the
// connect: the caller may still fall back to a public read, and reports reason
// only when that does not apply either.
type githubAppDeclined struct{ reason error }

func (d *githubAppDeclined) Error() string { return d.reason.Error() }

// resolveGitHubAppConnect connects through the deployment's App. It answers
// with a credential (the App covers the repository for this organization), a
// *githubAppDeclined (it does not), or any other error, which ends the connect.
//
// Only "no installation covers this repository" and "an installation covers it
// but this organization has not claimed it" decline. Anything else — GitHub
// failing, the claimed installation denying access — is returned as an error,
// so a transient App failure never quietly connects the source unauthenticated
// for good.
func (s *Service) resolveGitHubAppConnect(ctx context.Context, orgID, repo, branch string) (githubConnectCredential, error) {
	owner, name, _ := strings.Cut(repo, "/")
	installationID, err := s.githubConnector.FindRepositoryInstallation(ctx,
		githubconnector.AppCredential{AppID: s.githubAppID, PrivateKeyPEM: s.githubAppKeyPEM}, owner, name)
	if err != nil {
		var apiErr *githubconnector.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return githubConnectCredential{}, &githubAppDeclined{reason: status.Error(codes.FailedPrecondition,
				"No access token was supplied, the deployment's GitHub App is not installed on that repository, and the repository is not public. Install the App on it from this organization, or supply a repository-scoped fine-grained PAT.")}
		}
		return githubConnectCredential{}, githubInstallationTokenError(err)
	}

	var claimed bool
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		claimed, err = s.store.GitHubAppInstallationClaimedBy(ctx, installationID, orgID)
		return err
	}); err != nil {
		return githubConnectCredential{}, err
	}
	if !claimed {
		return githubConnectCredential{}, &githubAppDeclined{reason: status.Error(codes.PermissionDenied,
			"The GitHub App installation covering that repository is not connected to this organization. Install the App from this organization first, then connect the repository.")}
	}

	credential := githubStoredCredential{
		Kind:           githubCredentialKindApp,
		InstallationID: installationID,
		BoundAt:        time.Now().UTC().UnixNano(),
	}
	token, err := s.githubToken(ctx, credential, repo)
	if err != nil {
		return githubConnectCredential{}, err
	}
	if err := validateGitHubAccess(ctx, s.newGitHubClient(token), repo, branch); err != nil {
		return githubConnectCredential{}, err
	}
	blob, err := credential.marshal()
	if err != nil {
		return githubConnectCredential{}, err
	}
	return githubConnectCredential{Kind: githubCredentialKindApp, Plaintext: blob, InstallationID: installationID}, nil
}

// resolvePublicGitHubRepository asks GitHub, with no credential, whether repo is
// public, and when it is proves the source's branch is readable the same way —
// the exact requests the source will make for as long as it exists.
//
// GitHub answers an unauthenticated request for a private or missing repository
// with the same 404, so "not found" is "not public" and the caller refuses; it
// is never read as a reason to connect. Running out of the unauthenticated
// limit is reported as that, since no other answer would tell the caller that
// waiting, or supplying a credential, is the fix.
func (s *Service) resolvePublicGitHubRepository(ctx context.Context, repo, branch string) (bool, error) {
	if s.newGitHubClient == nil {
		return false, status.Error(codes.FailedPrecondition, "GitHub connector is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, githubAppSetupProbeTimeout)
	defer cancel()
	client := s.newGitHubClient("")
	public, err := client.RepositoryIsPublic(ctx, repo)
	switch {
	case errors.Is(err, github.ErrNotFound):
		return false, nil
	case err != nil:
		return false, githubValidationError(err)
	case !public:
		return false, nil
	}
	if err := validateGitHubAccess(ctx, client, repo, branch); err != nil {
		return false, err
	}
	return true, nil
}
