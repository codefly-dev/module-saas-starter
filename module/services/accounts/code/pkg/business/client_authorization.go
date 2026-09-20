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

// challengeMethodS256 is the only PKCE method accepted. "plain" puts the
// verifier itself in the authorization request, which defeats the point.
const challengeMethodS256 = "S256"

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
	UserID        string
	SessionID     string
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
	CreatedAt     time.Time
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
	w := wool.Get(ctx).In("IssueClientAuthorizationCode")
	store, hasStore := s.store.(ClientAuthorizationCodeStore)
	if !hasStore {
		return nil, w.NewError("store does not implement ClientAuthorizationCodeStore")
	}
	authorization := req.GetAuthorization()
	client, err := s.resolveClientAuthorization(authorization)
	if err != nil {
		return nil, err
	}

	caller, ok := auth.VerifiedRequestIdentity(ctx)
	if !ok || caller.EffectiveSubject == uuid.Nil || caller.SessionID == uuid.Nil {
		return nil, auth.ErrClientAuthorizationRejected
	}
	// An impersonated session may not hand a client a credential: the client
	// would hold a rotatable token for a person who never authorized it.
	if caller.Impersonated() {
		return nil, auth.ErrClientAuthorizationRejected
	}

	plaintext, hash, err := newClientAuthorizationCode()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate client authorization code")
	}
	now := time.Now()
	record := &ClientAuthorizationCode{
		ID:            NewIDString(),
		CodeHash:      hash,
		ClientID:      client.ClientID,
		RedirectURI:   authorization.GetRedirectUri(),
		CodeChallenge: authorization.GetCodeChallenge(),
		UserID:        caller.EffectiveSubjectID(),
		SessionID:     caller.SessionID.String(),
		ExpiresAt:     now.Add(clientAuthorizationCodeTTL),
		CreatedAt:     now,
	}
	if err := s.store.WithUserTx(ctx, record.UserID, func(ctx context.Context) error {
		return store.CreateClientAuthorizationCode(ctx, record)
	}); err != nil {
		return nil, w.Wrapf(err, "persist client authorization code")
	}

	s.emit(ctx, record.UserID, "user", EventAuthClientAuthorized,
		"session", record.SessionID, caller.OrgID.String(),
		map[string]any{"client_id": client.ClientID})

	return &gen.IssueClientAuthorizationCodeResponse{
		Code:      plaintext,
		ExpiresIn: expiresInSeconds(record.ExpiresAt),
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
	w := wool.Get(ctx).In("redeemClientAuthorizationCode")
	store, ok := s.store.(ClientAuthorizationCodeStore)
	if !ok {
		return nil, w.NewError("store does not implement ClientAuthorizationCodeStore")
	}

	var pair *auth.TokenPair
	var redeemed *ClientAuthorizationCode
	err := store.ConsumeClientAuthorizationCode(
		ctx,
		hashClientAuthorizationCode(grant.GetCode()),
		time.Now(),
		func(txCtx context.Context, code *ClientAuthorizationCode) error {
			// The code is consumed before any of this is checked, so a mismatch
			// burns it rather than leaving it to be guessed at again.
			if code.ClientID != client.ClientID ||
				code.RedirectURI != grant.GetRedirectUri() ||
				!verifyCodeChallenge(code.CodeChallenge, grant.GetCodeVerifier()) {
				return auth.ErrClientAuthorizationRejected
			}
			userID, err := uuid.Parse(code.UserID)
			if err != nil {
				return auth.ErrClientAuthorizationRejected
			}
			sessionID, err := uuid.Parse(code.SessionID)
			if err != nil {
				return auth.ErrClientAuthorizationRejected
			}
			pair, err = s.minter.MintForClient(txCtx, userID, sessionID, client.ClientID)
			if err != nil {
				return err
			}
			redeemed = code
			return nil
		},
	)
	if err != nil {
		if errors.Is(err, auth.ErrClientAuthorizationRejected) || errors.Is(err, auth.ErrSessionUnavailable) {
			return nil, auth.ErrClientAuthorizationRejected
		}
		return nil, w.Wrapf(err, "redeem client authorization code")
	}
	if pair == nil || redeemed == nil {
		return nil, auth.ErrClientAuthorizationRejected
	}

	s.emit(ctx, redeemed.UserID, "user", EventAuthLogin,
		"session", redeemed.SessionID, "",
		map[string]any{"client_id": client.ClientID})

	return clientTokenResponse(pair), nil
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
	pair, err := s.minter.VerifyClientRefresh(ctx, grant.GetRefreshToken(), client.ClientID)
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
	if request.GetCodeChallengeMethod() != challengeMethodS256 {
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
