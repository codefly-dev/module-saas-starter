package business

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

// clientAuthorizationCodeTTL is deliberately far shorter than any session
// lifetime. The code only has to survive one redirect back to a client that is
// already waiting for it.
const clientAuthorizationCodeTTL = time.Minute

// ClientAuthorizationCode is a one-time credential binding a signed-in person
// to the client they authorized. It carries no authority of its own: what it
// names is the host session, and redemption resolves the person's current
// authorization through that session rather than trusting anything recorded
// here.
type ClientAuthorizationCode struct {
	ID            string
	CodeHash      string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	// Resource is the RFC 8707 indicator the person approved, or empty. It is
	// recorded on the code and read back at redemption, so the audience the
	// client ends up holding is the one the authorization bound — never one the
	// client names for the first time at the token endpoint.
	Resource string
	// Scope is what was granted, echoed on the token response.
	Scope      string
	UserID     string
	SessionID  string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// ClientAuthorizationCodeStore persists authorization codes. Consume must lock
// the row, mark it consumed, and invoke redeem in the same database
// transaction, so a code races itself to exactly one session.
type ClientAuthorizationCodeStore interface {
	CreateClientAuthorizationCode(ctx context.Context, code *ClientAuthorizationCode) error
	ConsumeClientAuthorizationCode(
		ctx context.Context,
		codeHash string,
		now time.Time,
		redeem func(context.Context, *ClientAuthorizationCode) error,
	) error
}

// SetClientRegistry wires the declared first-party clients. Client sign-in
// fails closed when this is nil or empty.
func (s *Service) SetClientRegistry(registry *auth.ClientRegistry) {
	s.clientRegistry = registry
}

// ValidateClientAuthorization resolves a client's authorization request before
// the login page renders anything. Refusing here — while the browser is still
// on the host and no credentials have been entered — is the point: a client id
// nobody registered, or a redirect URI registered to some other client, never
// reaches a sign-in form, let alone a code.
func (s *Service) ValidateClientAuthorization(
	ctx context.Context,
	req *gen.ValidateClientAuthorizationRequest,
) (*gen.ValidateClientAuthorizationResponse, error) {
	client, err := s.resolveClientAuthorization(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	return &gen.ValidateClientAuthorizationResponse{ClientName: client.Name}, nil
}

// IssueClientAuthorizationCode mints the code the host redirects back to the
// client with. The caller is the signed-in person's own host session, so the
// code is bound to an identity the client never watched authenticate.
func (s *Service) IssueClientAuthorizationCode(
	ctx context.Context,
	req *gen.IssueClientAuthorizationCodeRequest,
) (*gen.IssueClientAuthorizationCodeResponse, error) {
	authorization := req.GetAuthorization()
	client, err := s.resolveClientAuthorization(authorization)
	if err != nil {
		return nil, err
	}
	// This RPC predates resource indicators and carries none, so the session it
	// authorizes is audience-unbound: exactly the credential the first
	// registered client was built against. A client that wants a resource-bound
	// token uses the standard authorize endpoint, which carries one.
	issued, err := s.issueAuthorizationCode(ctx, authorizationCodeGrant{
		Client:        client,
		RedirectURI:   authorization.GetRedirectUri(),
		CodeChallenge: authorization.GetCodeChallenge(),
	})
	if err != nil {
		return nil, err
	}
	return &gen.IssueClientAuthorizationCodeResponse{
		Code:      issued.Code,
		ExpiresIn: issued.ExpiresIn,
	}, nil
}

// ExchangeClientToken is the registered client's token endpoint. Both grants
// return the tokens in the response body: a public client has no cookie jar the
// host controls, and the refresh half rotates on every use so a leaked one is
// good for a single race the legitimate client then detects.
func (s *Service) ExchangeClientToken(
	ctx context.Context,
	req *gen.ExchangeClientTokenRequest,
) (*gen.ExchangeClientTokenResponse, error) {
	if s.minter == nil {
		return nil, auth.ErrClientAuthorizationRejected
	}
	// Registry only, deliberately: this RPC must not fetch a remote document.
	//
	// Its paired issue path (resolveClientAuthorization) is registry-only too,
	// so no metadata-document client can obtain a code through this flow, and
	// the standard endpoints resolve their own client without coming through
	// here. Resolving metadata documents here would therefore add an outbound
	// fetch for a caller-supplied URL — before any code is examined — to a
	// published RPC, for a capability nothing can reach.
	client, ok := s.clientRegistry.Lookup(req.GetClientId())
	if !ok {
		return nil, auth.ErrClientAuthorizationRejected
	}

	switch grant := req.GetGrant().(type) {
	case *gen.ExchangeClientTokenRequest_AuthorizationCode:
		return s.redeemClientAuthorizationCode(ctx, client, grant.AuthorizationCode)
	case *gen.ExchangeClientTokenRequest_RefreshToken:
		return s.rotateClientRefreshToken(ctx, client, grant.RefreshToken)
	default:
		return nil, auth.ErrClientAuthorizationRejected
	}
}

func (s *Service) redeemClientAuthorizationCode(
	ctx context.Context,
	client auth.RegisteredClient,
	grant *gen.AuthorizationCodeGrant,
) (*gen.ExchangeClientTokenResponse, error) {
	// One code consumption path, shared with the standard token endpoint. This
	// RPC's response shape is the older one (no token_type, no scope), so the
	// standard fields are projected away rather than a second redemption being
	// written beside the first.
	redeemed, err := s.redeemAuthorizationCode(ctx, client, authorizationCodeRedemption{
		Code:         grant.GetCode(),
		RedirectURI:  grant.GetRedirectUri(),
		CodeVerifier: grant.GetCodeVerifier(),
	})
	if err != nil {
		return nil, auth.ErrClientAuthorizationRejected
	}
	return &gen.ExchangeClientTokenResponse{
		AccessToken:  redeemed.AccessToken,
		RefreshToken: redeemed.RefreshToken,
		ExpiresIn:    redeemed.ExpiresIn,
	}, nil
}

func (s *Service) rotateClientRefreshToken(
	ctx context.Context,
	client auth.RegisteredClient,
	grant *gen.ClientRefreshTokenGrant,
) (*gen.ExchangeClientTokenResponse, error) {
	w := wool.Get(ctx).In("rotateClientRefreshToken")
	// Which client a refresh token belongs to is read from the locked session
	// row, not from the request, so one client naming another's token is
	// refused the same way a token that never existed is.
	// No resource: this RPC predates resource indicators and carries none, so a
	// resource-bound session rotates through it unchanged rather than being
	// refused for a parameter the caller cannot send.
	pair, err := s.minter.VerifyClientRefresh(ctx, grant.GetRefreshToken(), client.ClientID, "")
	if err != nil {
		if errors.Is(err, auth.ErrRefreshRevoked) || errors.Is(err, auth.ErrRefreshReuse) {
			return nil, auth.ErrClientAuthorizationRejected
		}
		return nil, w.Wrapf(err, "rotate client refresh token")
	}
	return clientTokenResponse(pair), nil
}

func clientTokenResponse(pair *auth.TokenPair) *gen.ExchangeClientTokenResponse {
	return &gen.ExchangeClientTokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    expiresInSeconds(pair.AccessTokenExpiresAt),
	}
}

// resolveClientAuthorization applies the whole registry check: a known client,
// a redirect URI that client registered, and the one supported PKCE method. All
// three collapse to a single sentinel so an unauthenticated caller cannot use
// the answers to enumerate the registry.
func (s *Service) resolveClientAuthorization(
	request *gen.ClientAuthorizationRequest,
) (auth.RegisteredClient, error) {
	if request == nil {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	client, ok := s.clientRegistry.Lookup(request.GetClientId())
	if !ok {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	if !client.AllowsRedirect(request.GetRedirectUri()) {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	if request.GetCodeChallengeMethod() != auth.ChallengeMethodS256 {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	if request.GetCodeChallenge() == "" {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	return client, nil
}

func newClientAuthorizationCode() (plaintext, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, hashClientAuthorizationCode(plaintext), nil
}

func hashClientAuthorizationCode(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}

// verifyCodeChallenge recomputes the S256 challenge from the verifier the
// client kept. Only the holder of the verifier can redeem the code, so a code
// intercepted in the redirect is useless on its own.
func verifyCodeChallenge(challenge, verifier string) bool {
	if challenge == "" || len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	digest := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// RegisteredClients returns the declared clients for the internal registry
// read. It serves configuration resolved at startup, so it needs no store and
// cannot fail; an unconfigured registry simply has nothing to report.
func (s *Service) RegisteredClients(_ context.Context) []auth.RegisteredClient {
	return s.clientRegistry.All()
}

// renderOrgID renders an organization for the audit trail, leaving an orgless
// session's zero id empty rather than spelling it as the nil UUID. A nil-UUID
// org_id is a tenant that does not exist: the row would be attributed to it,
// and readable by nobody. Mirrors RequestIdentity.RealActorID's handling of a
// zero actor.
func renderOrgID(orgID uuid.UUID) string {
	if orgID == uuid.Nil {
		return ""
	}
	return orgID.String()
}
