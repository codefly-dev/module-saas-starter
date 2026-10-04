package business

import (
	"context"
	"errors"
	"strings"
	"time"

	"accounts/pkg/auth"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

// The host as an OAuth 2.1 authorization server (issue #1003).
//
// This is not a second authorization server beside the registered-client flow:
// it is the standard spelling of the same one. The registry, the one-time code
// table, the PKCE check, the minter and the session kind are all the ones
// pkg/business/client_authorization.go already uses. What is added here is what
// a client that was written against the specifications rather than against this
// host needs in order to find and use it:
//
//   - RFC 8414 authorization-server metadata, so a client discovers the
//     endpoints instead of being configured with them.
//   - A `resource` parameter (RFC 8707) carried from the authorization request
//     into the token's audience, so a token minted for one solution's MCP
//     endpoint is refused at another's.
//   - Client ID Metadata Documents, so a client the operator never declared can
//     still sign a person in without anything durable being written.
//
// Deliberately absent: dynamic client registration (RFC 7591). The MCP
// specification deprecated it in favour of metadata documents, and it is the
// one mechanism here that would grow a durable registry on unauthenticated
// request — an attacker-sized table, and a registered display name that can
// impersonate a client the operator did meant to vouch for. The published
// metadata therefore carries no `registration_endpoint`, which is how a
// conforming client learns not to try.

// supportedOAuthScope is the one scope value the host accepts.
//
// It is `offline_access` because that is the one thing a scope could honestly
// say about a token this host issues: the credential is refreshable. Everything
// else a scope might narrow — which organisation, which permissions, which
// resources — the host resolves from the person's own authority at every call,
// and it does not narrow by scope at all. Publishing a scope the host does not
// enforce would be a capability claim nothing backs, so the list says only what
// is true, and any other requested value is refused by name rather than
// silently dropped (RFC 6749 §4.1.2.1 `invalid_scope`).
const supportedOAuthScope = "offline_access"

// OAuth 2.1 / RFC 6749 §5.2 error codes, as a client reads them.
const (
	OAuthErrorInvalidRequest   = "invalid_request"
	OAuthErrorInvalidClient    = "invalid_client"
	OAuthErrorInvalidGrant     = "invalid_grant"
	OAuthErrorUnsupportedGrant = "unsupported_grant_type"
	OAuthErrorInvalidScope     = "invalid_scope"
	OAuthErrorAccessDenied     = "access_denied"
	OAuthErrorInvalidTarget    = "invalid_target"
	OAuthErrorServerError      = "server_error"
)

// ErrOAuthIssuerUnavailable is the authorization server with no trusted public
// origin to call itself. Every document it publishes names that origin, and a
// metadata document served under the wrong issuer is worse than none: a client
// that fetched it would send authorization requests somewhere else. Fail closed.
var ErrOAuthIssuerUnavailable = errors.New("the authorization server has no trusted public origin")

// AuthorizationServerMetadata is the RFC 8414 document, in the field order and
// spelling the registry uses. It is produced from what this service actually
// enforces, so the document cannot claim a capability the code does not have.
type AuthorizationServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	JWKSURI                                    string   `json:"jwks_uri"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseISSParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
	ServiceDocumentation                       string   `json:"service_documentation,omitempty"`
}

// Public paths of the authorization server, as a client discovers them.
const (
	// OAuthAuthorizeEndpointPath is the standard authorization endpoint. It is
	// served by the frontend — the host's login page is the only sign-in UI, and
	// consent is a page, not a response body — which then drives the two RPCs
	// below.
	OAuthAuthorizeEndpointPath = "/oauth2/authorize"
	// OAuthTokenEndpointPath is the standard token endpoint, form-encoded per
	// RFC 6749 §3.2.
	OAuthTokenEndpointPath = "/oauth2/token"
	// OAuthJWKSPath is where the host already publishes its verification keys.
	OAuthJWKSPath = "/v1/auth/.well-known/jwks.json"
)

// AuthorizationServerMetadata builds the published document for this request's
// issuer. The issuer is the operator-trusted public base URL; without one the
// call fails rather than naming a host it cannot vouch for.
func (s *Service) AuthorizationServerMetadata(ctx context.Context) (*AuthorizationServerMetadata, error) {
	issuer := s.publicBaseURL(ctx)
	if issuer == "" {
		return nil, ErrOAuthIssuerUnavailable
	}
	return &AuthorizationServerMetadata{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + OAuthAuthorizeEndpointPath,
		TokenEndpoint:         issuer + OAuthTokenEndpointPath,
		JWKSURI:               issuer + OAuthJWKSPath,
		ScopesSupported:       []string{supportedOAuthScope},
		// `code` only. OAuth 2.1 removes the implicit and password grants, and
		// this host never had them.
		ResponseTypesSupported: []string{"code"},
		GrantTypesSupported:    []string{"authorization_code", "refresh_token"},
		// A public client has nowhere to keep a secret, so there is exactly one
		// way to authenticate at the token endpoint: not at all. Possession of
		// the code plus its PKCE verifier, or of the rotating refresh token, is
		// the credential.
		TokenEndpointAuthMethodsSupported: []string{"none"},
		// S256 only. `plain` puts the verifier in the authorization request,
		// which defeats the point, and OAuth 2.1 requires S256 for it.
		CodeChallengeMethodsSupported:     []string{auth.ChallengeMethodS256},
		ClientIDMetadataDocumentSupported: s.clientMetadata.Enabled(),
		// The authorization response carries `iss` (RFC 9207), so a client with
		// more than one configured authorization server cannot be made to
		// redeem one server's code at another.
		AuthorizationResponseISSParameterSupported: true,
	}, nil
}

// SetClientMetadataResolver wires Client ID Metadata Document support. Left
// unset, a metadata client_id is refused exactly as an unregistered slug is.
func (s *Service) SetClientMetadataResolver(resolver *auth.ClientMetadataResolver) {
	s.clientMetadata = resolver
}

// OAuthAuthorizationRequest is one client's authorization request, as the
// authorize endpoint received it. Every field is caller-supplied and none is
// trusted before ResolveOAuthAuthorization has returned.
type OAuthAuthorizationRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
	Resource            string
}

// ResolvedOAuthAuthorization is a request the host has accepted: which client,
// which resource, and what the person must be shown before a code is issued.
type ResolvedOAuthAuthorization struct {
	Client   auth.RegisteredClient
	Resource auth.ResourceIndicator
	Scope    string
	// RequiresConsent is true when the person must name-and-approve this client
	// before a code is issued.
	//
	// Two cases need it. A metadata client is one the operator never declared,
	// so nobody but the person can vouch for it. And a request naming a
	// resource is a request to narrow a credential to something specific, which
	// the person should see named — "this client, for that one solution" is a different
	// sentence from "Claude Code". An operator-declared client asking for
	// nothing in particular keeps the silent handoff it has today: the
	// deployment already vouched for it, and adding a prompt would change a
	// shipped client's flow for no gain in what the person can decide.
	RequiresConsent bool
}

// OAuthAuthorizationError is a refusal the authorize endpoint can report to the
// client, as RFC 6749 §4.1.2.1 requires: once the redirect URI is validated,
// errors go back to the client rather than being rendered to the person, and
// before it is validated they must NOT, or the endpoint becomes an open
// redirector.
type OAuthAuthorizationError struct {
	// Code is the RFC 6749 error code.
	Code string
	// Description is a short, non-enumerating explanation.
	Description string
	// Redirectable reports whether the redirect URI was validated before this
	// refusal, and so whether the error may be delivered to it.
	Redirectable bool
}

func (e *OAuthAuthorizationError) Error() string {
	return e.Code + ": " + e.Description
}

func authorizationError(code, description string, redirectable bool) *OAuthAuthorizationError {
	return &OAuthAuthorizationError{Code: code, Description: description, Redirectable: redirectable}
}

// ResolveOAuthAuthorization validates an authorization request completely,
// before any sign-in UI is shown: the client, its redirect URI, the PKCE
// challenge, the scope, and the resource indicator. It is callable
// unauthenticated, which is the point — a request that cannot succeed must be
// refused while the browser is still on the host and the person has typed
// nothing.
func (s *Service) ResolveOAuthAuthorization(
	ctx context.Context,
	request OAuthAuthorizationRequest,
) (*ResolvedOAuthAuthorization, error) {
	// The redirect URI is resolved first and everything else after it, so the
	// `Redirectable` flag on each refusal below is a fact about what has already
	// been checked rather than a judgement call at each site.
	client, err := s.resolveOAuthClient(ctx, request.ClientID)
	if err != nil {
		return nil, authorizationError(OAuthErrorInvalidClient,
			"this application is not registered to sign in here", false)
	}
	if !client.AllowsRedirect(request.RedirectURI) {
		return nil, authorizationError(OAuthErrorInvalidRequest,
			"this application asked to be returned to an address it has not registered", false)
	}

	// From here the redirect URI is the client's own, so a refusal is delivered
	// to it with `error` and `state`, which is what lets the client show the
	// person something better than a hung browser tab.
	if responseType := strings.TrimSpace(request.ResponseType); responseType != "" && responseType != "code" {
		return nil, authorizationError(OAuthErrorInvalidRequest,
			"only the authorization code response type is supported", true)
	}
	if request.CodeChallengeMethod != auth.ChallengeMethodS256 || request.CodeChallenge == "" {
		return nil, authorizationError(OAuthErrorInvalidRequest,
			"a PKCE S256 code challenge is required", true)
	}
	if !auth.ValidCodeChallenge(request.CodeChallenge) {
		return nil, authorizationError(OAuthErrorInvalidRequest,
			"the code challenge is malformed", true)
	}
	// `state` is not required by RFC 6749 and OAuth 2.1 makes PKCE the CSRF
	// defence, so a client that omits it is not refused — but one that sends a
	// value longer than the redirect can carry is, rather than having it
	// truncated into a value the client will not recognise.
	if len(request.State) > maxOAuthStateLength {
		return nil, authorizationError(OAuthErrorInvalidRequest,
			"the state value is too long", true)
	}
	scope, err := resolveOAuthScope(request.Scope)
	if err != nil {
		return nil, authorizationError(OAuthErrorInvalidScope,
			"this authorization server supports only the "+supportedOAuthScope+" scope", true)
	}

	resolved := &ResolvedOAuthAuthorization{Client: client, Scope: scope}
	if raw := strings.TrimSpace(request.Resource); raw != "" {
		issuer := s.publicBaseURL(ctx)
		if issuer == "" {
			return nil, authorizationError(OAuthErrorServerError,
				"the authorization server cannot determine its own address", true)
		}
		resource, err := auth.RequireResourceAtOrigin(raw, issuer)
		if err != nil {
			return nil, authorizationError(OAuthErrorInvalidTarget,
				"that resource is not one this host issues tokens for", true)
		}
		resolved.Resource = resource
	}
	resolved.RequiresConsent = client.Metadata || resolved.Resource.Value != ""
	return resolved, nil
}

// maxOAuthStateLength bounds the opaque value echoed back on the redirect. It
// is generous — a signed state token fits — and exists so the endpoint cannot be
// used to build an arbitrarily long URL.
const maxOAuthStateLength = 2048

// resolveOAuthScope applies the published scopes_supported. An absent scope is
// the supported one: the credential is refreshable whether or not the client
// asked, so answering "no scope" with a token that has a refresh half and an
// empty `scope` would under-report what was granted.
func resolveOAuthScope(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return supportedOAuthScope, nil
	}
	for _, value := range strings.Fields(requested) {
		if value != supportedOAuthScope {
			return "", errors.New("unsupported scope")
		}
	}
	return supportedOAuthScope, nil
}

// resolveOAuthClient resolves a presented client_id through the operator's
// registry, falling back to a Client ID Metadata Document when the id is an
// https URL. The two namespaces cannot collide — a registry slug can never
// spell `https://` — so there is no precedence question to get wrong.
func (s *Service) resolveOAuthClient(ctx context.Context, clientID string) (auth.RegisteredClient, error) {
	clientID = strings.TrimSpace(clientID)
	if auth.IsMetadataClientID(clientID) {
		client, err := s.clientMetadata.Resolve(ctx, clientID)
		if err != nil {
			// The named reason is logged for the operator and collapsed for the
			// caller: an unauthenticated browser must not learn which documents
			// this deployment would admit.
			wool.Get(ctx).In("resolveOAuthClient").Debug("client metadata document refused",
				wool.Field("client_id", clientID), wool.ErrField(err))
			return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
		}
		return client, nil
	}
	if !auth.ValidClientID(clientID) {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	client, ok := s.clientRegistry.Lookup(clientID)
	if !ok {
		return auth.RegisteredClient{}, auth.ErrClientAuthorizationRejected
	}
	return client, nil
}

// GrantOAuthAuthorization issues the one-time code for an authorization request
// the signed-in person has approved. The caller is that person's own host
// session — the same requirement IssueClientAuthorizationCode has — so the code
// names an identity the client never watched authenticate.
func (s *Service) GrantOAuthAuthorization(
	ctx context.Context,
	request OAuthAuthorizationRequest,
) (code string, expiresIn int64, err error) {
	resolved, err := s.ResolveOAuthAuthorization(ctx, request)
	if err != nil {
		return "", 0, err
	}
	issued, err := s.issueAuthorizationCode(ctx, authorizationCodeGrant{
		Client:        resolved.Client,
		RedirectURI:   request.RedirectURI,
		CodeChallenge: request.CodeChallenge,
		Resource:      resolved.Resource.Value,
		Scope:         resolved.Scope,
	})
	if err != nil {
		return "", 0, err
	}
	return issued.Code, issued.ExpiresIn, nil
}

// OAuthTokenRequest is one token-endpoint request, as RFC 6749 §3.2 delivers
// it: form-encoded, with the grant type selecting which of the other fields
// matter.
type OAuthTokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Resource     string
	Scope        string
}

// OAuthTokenResponse is the RFC 6749 §5.1 response. `token_type` is required by
// the specification and was missing from this host's own token RPC, which is
// why a client written against a standard library could not use it.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// ExchangeOAuthToken is the standard token endpoint over the registered-client
// exchange. Both grants return the credential in the body: a public client has
// no cookie jar the host controls, and the refresh half rotates on every use.
func (s *Service) ExchangeOAuthToken(
	ctx context.Context,
	request OAuthTokenRequest,
) (*OAuthTokenResponse, error) {
	switch strings.TrimSpace(request.GrantType) {
	case "authorization_code":
		return s.exchangeOAuthAuthorizationCode(ctx, request)
	case "refresh_token":
		return s.exchangeOAuthRefreshToken(ctx, request)
	case "":
		return nil, authorizationError(OAuthErrorInvalidRequest, "grant_type is required", false)
	default:
		return nil, authorizationError(OAuthErrorUnsupportedGrant,
			"this authorization server supports the authorization_code and refresh_token grants", false)
	}
}

func (s *Service) exchangeOAuthAuthorizationCode(
	ctx context.Context,
	request OAuthTokenRequest,
) (*OAuthTokenResponse, error) {
	client, err := s.resolveOAuthClient(ctx, request.ClientID)
	if err != nil {
		return nil, authorizationError(OAuthErrorInvalidClient, "unknown client", false)
	}
	redeemed, err := s.redeemAuthorizationCode(ctx, client, authorizationCodeRedemption{
		Code:         request.Code,
		RedirectURI:  request.RedirectURI,
		CodeVerifier: request.CodeVerifier,
		// RFC 8707 §2.2: a token request may repeat `resource`, and it must be
		// one the authorization granted. It is checked against the code's own
		// binding rather than being allowed to set it, so a client cannot widen
		// at redemption what the person approved at authorization.
		Resource: strings.TrimSpace(request.Resource),
	})
	if err != nil {
		if invalid := new(OAuthAuthorizationError); errors.As(err, &invalid) {
			return nil, err
		}
		return nil, authorizationError(OAuthErrorInvalidGrant,
			"the authorization code is invalid, expired, or already used", false)
	}
	return redeemed, nil
}

func (s *Service) exchangeOAuthRefreshToken(
	ctx context.Context,
	request OAuthTokenRequest,
) (*OAuthTokenResponse, error) {
	client, err := s.resolveOAuthClient(ctx, request.ClientID)
	if err != nil {
		return nil, authorizationError(OAuthErrorInvalidClient, "unknown client", false)
	}
	if strings.TrimSpace(request.RefreshToken) == "" {
		return nil, authorizationError(OAuthErrorInvalidRequest, "refresh_token is required", false)
	}
	// The resource is checked on the locked session row, before the token is
	// consumed: a client naming the wrong one is refused and still holds the
	// credential it legitimately has. Checking it after the rotation — on the
	// token just minted — would have cost that client its session for sending a
	// parameter RFC 8707 only lets it repeat.
	pair, err := s.minter.VerifyClientRefresh(
		ctx, request.RefreshToken, client.ClientID, strings.TrimSpace(request.Resource))
	if err != nil {
		if errors.Is(err, auth.ErrRefreshResourceMismatch) {
			return nil, authorizationError(OAuthErrorInvalidTarget,
				"this refresh token is not bound to that resource", false)
		}
		return nil, authorizationError(OAuthErrorInvalidGrant,
			"the refresh token is invalid, expired, or has been revoked", false)
	}
	return oauthTokenResponse(pair, supportedOAuthScope), nil
}

func oauthTokenResponse(pair *auth.TokenPair, scope string) *OAuthTokenResponse {
	return &OAuthTokenResponse{
		AccessToken:  pair.AccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    expiresInSeconds(pair.AccessTokenExpiresAt),
		RefreshToken: pair.RefreshToken,
		Scope:        scope,
	}
}

// authorizationCodeGrant is what issueAuthorizationCode needs: a resolved
// client plus the request fields the code is bound to.
type authorizationCodeGrant struct {
	Client        auth.RegisteredClient
	RedirectURI   string
	CodeChallenge string
	Resource      string
	Scope         string
}

type issuedAuthorizationCode struct {
	Code      string
	ExpiresIn int64
}

// issueAuthorizationCode is the one place a code is minted, shared by the
// standard authorize endpoint and by the IssueClientAuthorizationCode RPC the
// first registered client was built against.
func (s *Service) issueAuthorizationCode(
	ctx context.Context,
	grant authorizationCodeGrant,
) (*issuedAuthorizationCode, error) {
	w := wool.Get(ctx).In("issueAuthorizationCode")
	store, hasStore := s.store.(ClientAuthorizationCodeStore)
	if !hasStore {
		return nil, w.NewError("store does not implement ClientAuthorizationCodeStore")
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
		ClientID:      grant.Client.ClientID,
		RedirectURI:   grant.RedirectURI,
		CodeChallenge: grant.CodeChallenge,
		Resource:      grant.Resource,
		Scope:         grant.Scope,
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

	payload := map[string]any{"client_id": grant.Client.ClientID}
	if grant.Resource != "" {
		payload["resource"] = grant.Resource
	}
	if grant.Client.Metadata {
		// Which clients signed in by publishing a document, rather than by
		// being declared, is exactly the thing an operator who enabled the
		// mechanism needs to be able to read back out of the audit trail.
		payload["client_registration"] = "metadata_document"
	}
	s.emit(ctx, record.UserID, "user", EventAuthClientAuthorized,
		"session", record.SessionID, renderOrgID(caller.OrgID), payload)

	return &issuedAuthorizationCode{
		Code:      plaintext,
		ExpiresIn: expiresInSeconds(record.ExpiresAt),
	}, nil
}

// authorizationCodeRedemption is what a token request presents for the
// authorization-code grant.
type authorizationCodeRedemption struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
	Resource     string
}

// redeemAuthorizationCode consumes a code and mints the client's session. It is
// shared with ExchangeClientToken, so there is one code consumption path, one
// PKCE check, and one place the session is created.
func (s *Service) redeemAuthorizationCode(
	ctx context.Context,
	client auth.RegisteredClient,
	presented authorizationCodeRedemption,
) (*OAuthTokenResponse, error) {
	w := wool.Get(ctx).In("redeemAuthorizationCode")
	store, ok := s.store.(ClientAuthorizationCodeStore)
	if !ok {
		return nil, w.NewError("store does not implement ClientAuthorizationCodeStore")
	}
	if s.minter == nil {
		return nil, auth.ErrClientAuthorizationRejected
	}

	var pair *auth.TokenPair
	var redeemed *ClientAuthorizationCode
	var mismatch *OAuthAuthorizationError
	err := store.ConsumeClientAuthorizationCode(
		ctx,
		hashClientAuthorizationCode(presented.Code),
		time.Now(),
		func(txCtx context.Context, code *ClientAuthorizationCode) error {
			// The code is consumed before any of this is checked, so a mismatch
			// burns it rather than leaving it to be guessed at again.
			if code.ClientID != client.ClientID ||
				code.RedirectURI != presented.RedirectURI ||
				!verifyCodeChallenge(code.CodeChallenge, presented.CodeVerifier) {
				return auth.ErrClientAuthorizationRejected
			}
			// A repeated `resource` must be the one the code was issued for.
			// Naming a different one is reported as invalid_target rather than
			// as a bad grant, because the grant was fine and the client's own
			// request is what disagrees with it.
			if presented.Resource != "" && presented.Resource != code.Resource {
				mismatch = authorizationError(OAuthErrorInvalidTarget,
					"this authorization was not granted for that resource", false)
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
			// The code's consumption and the client's session must commit
			// together: the marker is what lets the session store reuse this
			// transaction instead of opening its own, which would commit the
			// session while the code stayed redeemable.
			pair, err = s.minter.MintForClient(
				auth.WithAtomicSessionTransaction(txCtx), userID, sessionID,
				client.ClientID, code.Resource)
			if err != nil {
				return err
			}
			redeemed = code
			return nil
		},
	)
	if mismatch != nil {
		return nil, mismatch
	}
	if err != nil {
		if errors.Is(err, auth.ErrClientAuthorizationRejected) || errors.Is(err, auth.ErrSessionUnavailable) {
			return nil, auth.ErrClientAuthorizationRejected
		}
		return nil, w.Wrapf(err, "redeem client authorization code")
	}
	if pair == nil || redeemed == nil {
		return nil, auth.ErrClientAuthorizationRejected
	}

	// The audit row describes the session this exchange created, not the host
	// session that authorized it, and it must carry that session's organization:
	// audit_events' tenant policy matches on a non-null org_id, so a row emitted
	// without one is readable by no tenant at all. The minted token is the
	// authoritative record of both.
	minted, err := s.minter.VerifyAccess(pair.AccessToken)
	if err != nil {
		return nil, w.Wrapf(err, "verify freshly minted client access token")
	}
	payload := map[string]any{"client_id": client.ClientID}
	if redeemed.Resource != "" {
		payload["resource"] = redeemed.Resource
	}
	s.emit(ctx, redeemed.UserID, "user", EventAuthLogin,
		"session", minted.SessionID.String(), renderOrgID(minted.OrgID), payload)

	scope := redeemed.Scope
	if scope == "" {
		scope = supportedOAuthScope
	}
	return oauthTokenResponse(pair, scope), nil
}
