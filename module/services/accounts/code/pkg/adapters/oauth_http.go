package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
)

// The OAuth 2.1 / MCP authorization-server surface.
//
// These are raw HTTP handlers rather than proto RPCs on purpose, and not for
// convenience: RFC 6749 §3.2 requires the token endpoint to accept
// `application/x-www-form-urlencoded`, and §5.2 requires a specific JSON error
// body with a specific status. grpc-gateway produces neither, so an MCP client
// written against a standard OAuth library could not use a transcoded RPC — the
// shape IS the contract here. The business logic underneath is the same
// registered-client flow pkg/business/client_authorization.go serves.
//
// Public paths at the host's edge, which the frontend maps onto these:
//
//	GET  /.well-known/oauth-authorization-server -> /v1/oauth2/authorization-server
//	POST /oauth2/token                           -> /v1/oauth2/token
//	GET  /oauth2/authorize                       -> the frontend's own page, which
//	                                                drives the two authorize calls
//
// See module/services/frontend/code/src/proxy.ts for that mapping and
// MODULE.md for why the public shape lives at the edge.

// OAuthRoutePrefix is the namespace these paths live in. It is NOT what the
// handler is mounted at: RegisterHTTPRoute matches by prefix, so mounting the
// namespace would claim every path under it, including ones nothing declares at
// the gateway — and the correspondence gate in module/tools reads a registered
// prefix as a promise that the gateway routes something below it.
//
// The namespace must stay disjoint from every other registered route in BOTH
// directions, because combineHandlers iterates a map and an overlap would
// resolve nondeterministically. `/v1/auth/oauth/begin` — the host's own hop to
// an identity provider — is the near miss, which is why this is `/v1/oauth2/`
// rather than anything under `/v1/auth/oauth/`.
const OAuthRoutePrefix = "/v1/oauth2/"

// The exact paths. work.go registers each one on its own, so the surface the
// service registers is exactly the surface the gateway declares. Every request
// is still matched against this set in the handler, because a prefix match
// would otherwise serve `/v1/oauth2/token/anything` from the token endpoint.
const (
	OAuthMetadataPath          = "/v1/oauth2/authorization-server"
	OAuthAuthorizeValidatePath = "/v1/oauth2/authorize/validate"
	OAuthAuthorizeGrantPath    = "/v1/oauth2/authorize/grant"
	OAuthTokenPath             = "/v1/oauth2/token"
)

// NewOAuthHTTPHandler serves the authorization-server surface.
func NewOAuthHTTPHandler(svc *business.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withTrustedPublicOrigin(r))
		switch r.URL.Path {
		case OAuthMetadataPath:
			serveAuthorizationServerMetadata(svc, w, r)
		case OAuthAuthorizeValidatePath:
			serveOAuthAuthorizeValidate(svc, w, r)
		case OAuthAuthorizeGrantPath:
			serveOAuthAuthorizeGrant(svc, w, r)
		case OAuthTokenPath:
			serveOAuthToken(svc, w, r)
		default:
			writeJSONError(w, http.StatusNotFound, "not found")
		}
	})
}

// withTrustedPublicOrigin records the browser origin this host is reachable at,
// but only when the gateway credential is present beside it. Every document the
// authorization server publishes names that origin, so the one path that must
// not exist is an unauthenticated caller choosing it with a header: a metadata
// document served under an attacker's issuer would send the next client's
// authorization request there.
//
// These are raw HTTP routes, so they do not pass through the Connect or gRPC
// interceptors that normally do this — the same rule is applied here rather
// than assumed. A configured APP_BASE_URL still wins over it inside the
// service (see publicBaseURL), which is what a deployment pins when it does not
// want a per-request value at all.
func withTrustedPublicOrigin(r *http.Request) context.Context {
	ctx := r.Context()
	if !singleValidGatewayToken(r.Header.Values("X-Codefly-Gateway-Token")) {
		return ctx
	}
	origin := r.Header.Get("X-Codefly-Public-Origin")
	if origin == "" {
		return ctx
	}
	stamped, err := auth.WithVerifiedPublicOrigin(ctx, origin)
	if err != nil {
		return ctx
	}
	return stamped
}

// maxOAuthRequestBytes bounds every body read here. These are all small,
// fixed-shape requests; the bound exists so an unauthenticated endpoint cannot
// be made to buffer.
const maxOAuthRequestBytes = 16 << 10

func serveAuthorizationServerMetadata(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	metadata, err := svc.AuthorizationServerMetadata(r.Context())
	if err != nil {
		// No trusted public origin means every URL in the document would be a
		// guess. A client that fetched such a document would send authorization
		// requests to whatever it named, so this answers 503 and says why
		// rather than publishing one.
		wool.Get(r.Context()).In("oauthMetadata").Warn("cannot publish authorization-server metadata",
			wool.ErrField(err))
		writeJSONError(w, http.StatusServiceUnavailable,
			"the authorization server has no configured public address")
		return
	}
	// RFC 8414 §3.2: the document is cacheable. Five minutes is short enough
	// that enabling metadata clients or changing the public origin takes effect
	// while a person is still trying, and long enough that a client's discovery
	// chain costs one fetch per session rather than one per request.
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, metadata)
}

// oauthAuthorizeRequestBody is the authorization request as the frontend's
// `/oauth2/authorize` handler and its login page send it on. The field names
// are the OAuth parameter names, so what the browser received and what accounts
// validates are spelled the same and cannot drift in transcription.
type oauthAuthorizeRequestBody struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	State               string `json:"state"`
	Scope               string `json:"scope"`
	Resource            string `json:"resource"`
	ConsentGranted      bool   `json:"consent_granted"`
}

func (b oauthAuthorizeRequestBody) request() business.OAuthAuthorizationRequest {
	return business.OAuthAuthorizationRequest{
		ResponseType:        b.ResponseType,
		ClientID:            b.ClientID,
		RedirectURI:         b.RedirectURI,
		CodeChallenge:       b.CodeChallenge,
		CodeChallengeMethod: b.CodeChallengeMethod,
		State:               b.State,
		Scope:               b.Scope,
		Resource:            b.Resource,
		ConsentGranted:      b.ConsentGranted,
	}
}

// oauthAuthorizeValidateResponse is what the page needs to render: who is
// asking, where from, for what, and whether the person must approve it. The
// registry itself — other redirect URIs, the client's CORS origins — is never
// served to a browser.
//
// The field names are the wire contract with the frontend, which DECODES them
// (features/auth/model/oauth-authorization.ts). They were once read by casting
// this JSON to a camelCase interface, which type-checked, compiled, and made
// `requires_consent` read as undefined — so consent was skipped for every
// client that needed it. Renaming a field here without changing the decoder
// there breaks that seam silently again; the cross-seam test is what catches it.
type oauthAuthorizeValidateResponse struct {
	// ClientName is untrusted presentation, from the client's own document.
	ClientName string `json:"client_name"`
	// ClientOrigin is the origin this host VERIFIED, for a metadata client the
	// one it fetched the document from. Never the document's own `client_uri`.
	ClientOrigin    string `json:"client_origin"`
	ClientSource    string `json:"client_source"`
	Resource        string `json:"resource,omitempty"`
	ResourceName    string `json:"resource_name,omitempty"`
	Scope           string `json:"scope"`
	RequiresConsent bool   `json:"requires_consent"`
	// Issuer is this authorization server's own identity, so the page can echo
	// `iss` on the redirect it builds (RFC 9207) — including the decline.
	Issuer string `json:"issuer"`
}

func serveOAuthAuthorizeValidate(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	body, ok := decodeOAuthAuthorizeBody(w, r)
	if !ok {
		return
	}
	resolved, err := svc.ResolveOAuthAuthorization(r.Context(), body.request())
	if err != nil {
		writeOAuthAuthorizationError(w, svc.OAuthIssuer(), err)
		return
	}
	source := "registry"
	origin := resolved.Client.Origin
	if resolved.Client.Metadata {
		source = "metadata_document"
	} else if origin == "" {
		// An operator-declared client has no fetched origin to verify, and the
		// page must still have something non-empty to show: the deployment
		// vouched for this client by the id it declared, so that id is the
		// verified fact about it.
		origin = resolved.Client.ClientID
	}
	writeJSON(w, http.StatusOK, oauthAuthorizeValidateResponse{
		ClientName:      resolved.Client.Name,
		ClientOrigin:    origin,
		ClientSource:    source,
		Resource:        resolved.Resource.Value,
		ResourceName:    resolved.Resource.SolutionID,
		Scope:           resolved.Scope,
		RequiresConsent: resolved.RequiresConsent,
		Issuer:          svc.OAuthIssuer(),
	})
}

type oauthAuthorizeGrantResponse struct {
	Code      string `json:"code"`
	ExpiresIn int64  `json:"expires_in"`
	// Issuer is echoed so the page puts `iss` on the redirect (RFC 9207). A
	// client configured with more than one authorization server uses it to
	// refuse a code that came back from the wrong one.
	Issuer string `json:"issuer"`
}

func serveOAuthAuthorizeGrant(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	body, ok := decodeOAuthAuthorizeBody(w, r)
	if !ok {
		return
	}
	// The caller is the signed-in person's own host session, verified the one
	// way every non-proto route here verifies: forwarded identity beside a
	// valid gateway credential, or the bearer checked locally.
	//
	// Without the tenant requirement, matching IssueClientAuthorizationCode's
	// TENANT_REQUIREMENT_NONE: the code names the person and redemption resolves
	// their current authorization through their session, so an orgless person
	// authorizing a client is an ordinary state this flow has always served.
	ctx, err := authenticateHTTPSession(svc, r)
	if err != nil {
		writeBillingAuthnError(w, r, err)
		return
	}
	code, expiresIn, err := svc.GrantOAuthAuthorization(ctx, body.request())
	if err != nil {
		writeOAuthAuthorizationError(w, svc.OAuthIssuer(), err)
		return
	}
	metadata, err := svc.AuthorizationServerMetadata(ctx)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"the authorization server has no configured public address")
		return
	}
	writeJSON(w, http.StatusOK, oauthAuthorizeGrantResponse{
		Code:      code,
		ExpiresIn: expiresIn,
		Issuer:    metadata.Issuer,
	})
}

func decodeOAuthAuthorizeBody(w http.ResponseWriter, r *http.Request) (oauthAuthorizeRequestBody, bool) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return oauthAuthorizeRequestBody{}, false
	}
	var body oauthAuthorizeRequestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOAuthRequestBytes)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return oauthAuthorizeRequestBody{}, false
	}
	return body, true
}

// oauthErrorBody is the RFC 6749 §5.2 error object. It is the same shape at
// the authorize and token endpoints, which is why a client can read either.
type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// writeOAuthAuthorizationError answers an authorize-side refusal. The response
// carries the machine-readable code and the `redirectable` fact, because only
// the frontend knows whether it is holding a browser it may send to the
// client's redirect URI — and sending one there before the URI is validated is
// how an authorization endpoint becomes an open redirector.
func writeOAuthAuthorizationError(w http.ResponseWriter, issuer string, err error) {
	var refusal *business.OAuthAuthorizationError
	if !errors.As(err, &refusal) {
		if errors.Is(err, auth.ErrClientAuthorizationRejected) {
			refusal = &business.OAuthAuthorizationError{
				Code:        business.OAuthErrorInvalidClient,
				Description: "this application is not registered to sign in here",
			}
		} else {
			writeJSONError(w, http.StatusInternalServerError, "authorization failed")
			return
		}
	}
	status := http.StatusBadRequest
	switch refusal.Code {
	case business.OAuthErrorInvalidClient:
		// 400, not 401. There is no client authentication at this endpoint to
		// have failed, and a 401 would make a browser prompt for credentials.
		status = http.StatusBadRequest
	case business.OAuthErrorServerError:
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, struct {
		oauthErrorBody
		Redirectable bool   `json:"redirectable"`
		Issuer       string `json:"issuer,omitempty"`
	}{
		oauthErrorBody: oauthErrorBody{Error: refusal.Code, ErrorDescription: refusal.Description},
		Redirectable:   refusal.Redirectable,
		// RFC 9207 §2 covers error responses, and this host advertises support
		// for the parameter: a client enforcing that protection cannot
		// authenticate an error redirect that carries no `iss`. The frontend
		// echoes it on the redirect it builds.
		Issuer: issuer,
	})
}

func serveOAuthToken(svc *business.Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthTokenError(w, http.StatusMethodNotAllowed,
			business.OAuthErrorInvalidRequest, "the token endpoint takes POST")
		return
	}
	// RFC 6749 §3.2: the request body is form-encoded. A JSON body is accepted
	// too, because the host's own frontend and its first registered client speak
	// JSON everywhere else and a 400 for the wrong content type would be a trap
	// with no security value — the credential is in the body either way.
	request, err := decodeOAuthTokenRequest(w, r)
	if err != nil {
		description := "the request body could not be read"
		if errors.Is(err, errDuplicateOAuthParameter) {
			description = "a request parameter was given more than once"
		}
		writeOAuthTokenError(w, http.StatusBadRequest,
			business.OAuthErrorInvalidRequest, description)
		return
	}
	// Authorization-code and refresh-token responses must not be cached by
	// anything between here and the client: they ARE the credential.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	tokens, err := svc.ExchangeOAuthToken(r.Context(), request)
	if err != nil {
		var refusal *business.OAuthAuthorizationError
		switch {
		case errors.As(err, &refusal):
			status := http.StatusBadRequest
			if refusal.Code == business.OAuthErrorServerError {
				status = http.StatusServiceUnavailable
			}
			writeOAuthTokenError(w, status, refusal.Code, refusal.Description)
		case errors.Is(err, auth.ErrClientAuthorizationRejected):
			writeOAuthTokenError(w, http.StatusBadRequest, business.OAuthErrorInvalidGrant,
				"the grant is invalid, expired, or already used")
		case errors.Is(err, auth.ErrRevocationUnavailable):
			writeOAuthTokenError(w, http.StatusServiceUnavailable, business.OAuthErrorServerError,
				"authorization is temporarily unavailable")
		default:
			wool.Get(r.Context()).In("oauthToken").Error("token exchange failed", wool.ErrField(err))
			writeOAuthTokenError(w, http.StatusBadRequest, business.OAuthErrorInvalidGrant,
				"the grant is invalid, expired, or already used")
		}
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func decodeOAuthTokenRequest(w http.ResponseWriter, r *http.Request) (business.OAuthTokenRequest, error) {
	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.EqualFold(strings.TrimSpace(contentType), "application/json") {
		var body struct {
			GrantType    string `json:"grant_type"`
			ClientID     string `json:"client_id"`
			Code         string `json:"code"`
			RedirectURI  string `json:"redirect_uri"`
			CodeVerifier string `json:"code_verifier"`
			RefreshToken string `json:"refresh_token"`
			Resource     string `json:"resource"`
			Scope        string `json:"scope"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOAuthRequestBytes)).Decode(&body); err != nil {
			return business.OAuthTokenRequest{}, err
		}
		return business.OAuthTokenRequest{
			GrantType:    body.GrantType,
			ClientID:     body.ClientID,
			Code:         body.Code,
			RedirectURI:  body.RedirectURI,
			CodeVerifier: body.CodeVerifier,
			RefreshToken: body.RefreshToken,
			Resource:     body.Resource,
			Scope:        body.Scope,
		}, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthRequestBytes)
	if err := r.ParseForm(); err != nil {
		return business.OAuthTokenRequest{}, err
	}
	// RFC 6749 §3.2: request parameters MUST NOT be included more than once.
	// `Get` reads the first and discards the rest, which turns a duplicated
	// parameter into a silent choice between two values a caller sent — so a
	// request carrying one is refused instead.
	//
	// `resource` is in the singleton set here even though RFC 8707 §2 allows it
	// repeated: this host serves exactly one resource shape per token, so two
	// distinct targets cannot both be honoured, and discarding the second would
	// mint a token for a resource the client did not think it asked for.
	for _, name := range []string{
		"grant_type", "client_id", "code", "redirect_uri",
		"code_verifier", "refresh_token", "resource", "scope",
	} {
		if len(r.PostForm[name]) > 1 {
			return business.OAuthTokenRequest{}, fmt.Errorf(
				"%w: %s", errDuplicateOAuthParameter, name)
		}
	}
	return business.OAuthTokenRequest{
		GrantType:    r.PostForm.Get("grant_type"),
		ClientID:     r.PostForm.Get("client_id"),
		Code:         r.PostForm.Get("code"),
		RedirectURI:  r.PostForm.Get("redirect_uri"),
		CodeVerifier: r.PostForm.Get("code_verifier"),
		RefreshToken: r.PostForm.Get("refresh_token"),
		Resource:     r.PostForm.Get("resource"),
		Scope:        r.PostForm.Get("scope"),
	}, nil
}

// errDuplicateOAuthParameter is a request carrying a singleton parameter twice.
var errDuplicateOAuthParameter = errors.New("duplicate OAuth parameter")

func writeOAuthTokenError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, oauthErrorBody{Error: code, ErrorDescription: description})
}
