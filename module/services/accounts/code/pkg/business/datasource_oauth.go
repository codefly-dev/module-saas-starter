package business

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"accounts/pkg/datasource/apisource"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DatasourceOAuthProvider is one deployment-wide app registration. Only the
// public endpoint metadata is projected into the provider descriptor.
type DatasourceOAuthProvider struct {
	AuthorizeURL string   `json:"authorize_url"`
	TokenURL     string   `json:"token_url"`
	Scopes       []string `json:"scopes"`
	ClientID     string   `json:"-"`
	ClientSecret string   `json:"-"`
}

// ConfigureDatasourceOAuth loads values and secrets delivered by Codefly. The
// maps share connector keys. An empty registration disables generic sign-in.
func (s *Service) ConfigureDatasourceOAuth(descriptors, clientIDs, clientSecrets string) error {
	providers := map[string]DatasourceOAuthProvider{}
	ids := map[string]string{}
	secrets := map[string]string{}
	for _, entry := range []struct {
		raw   string
		value any
	}{{descriptors, &providers}, {clientIDs, &ids}, {clientSecrets, &secrets}} {
		if strings.TrimSpace(entry.raw) != "" {
			if err := json.Unmarshal([]byte(entry.raw), entry.value); err != nil {
				return errors.New("invalid datasource OAuth configuration")
			}
		}
	}
	for key, p := range providers {
		for _, raw := range []string{p.AuthorizeURL, p.TokenURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
				return errors.New("datasource OAuth endpoints must be absolute HTTPS URLs")
			}
		}
		p.ClientID = ids[key]
		p.ClientSecret = secrets[key]
		if p.ClientID == "" {
			return errors.New("datasource OAuth client id required")
		}
		providers[key] = p
	}
	s.datasourceOAuth = providers
	return nil
}

type datasourceOAuthStateStore interface {
	ConsumeDatasourceOAuthState(context.Context, string, string, time.Time) (bool, error)
}
type oauthLinkStateKey struct{}
type oauthLinkState struct {
	claims        *datasourceLinkClaims
	state         string
	credentialRef *string
}

type oauthAccountLinker struct {
	s          *Service
	descriptor DatasourceOAuthProvider
}

// The verifier is a PRF of the signed, nonce-bearing state under the host's
// existing secret key. It never appears in that state or a database row.
func (s *Service) oauthVerifier(state string) string {
	mac := hmac.New(sha256.New, s.datasourceLinkKey)
	mac.Write([]byte("datasource-oauth-pkce\x00"))
	mac.Write([]byte(state))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (l oauthAccountLinker) AuthorizeURL(state, redirect string) string {
	u, err := url.Parse(l.descriptor.AuthorizeURL)
	if err != nil {
		return ""
	}
	challenge := sha256.Sum256([]byte(l.s.oauthVerifier(state)))
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", l.descriptor.ClientID)
	q.Set("state", state)
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(l.descriptor.Scopes, " "))
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	u.RawQuery = q.Encode()
	return u.String()
}
func (l oauthAccountLinker) ResolveAccount(ctx context.Context, code string) (string, string, error) {
	state, ok := ctx.Value(oauthLinkStateKey{}).(oauthLinkState)
	if !ok || state.claims.SourceID == "" {
		return "", "", errDatasourceLinkStateRejected
	}
	claims := state.claims
	store, ok := l.s.store.(datasourceOAuthStateStore)
	if !ok {
		return "", "", errors.New("OAuth state store unavailable")
	}
	// Consume before contacting the provider, in a committed transaction. A lost
	// token reply requires a fresh browser sign-in; the code is never dispatched twice.
	err := l.s.store.WithOrgTx(ctx, claims.OrgID, func(ctx context.Context) error {
		source, err := l.s.store.GetDatasourceSource(ctx, claims.OrgID, claims.SourceID)
		if err != nil || source == nil || source.PersonalOwnerUserID != claims.UserID || source.Provider != claims.Connector {
			return errDatasourceLinkStateRejected
		}
		sum := sha256.Sum256([]byte(state.state))
		consumed, err := store.ConsumeDatasourceOAuthState(ctx, claims.OrgID, hex.EncodeToString(sum[:]), time.Unix(claims.ExpiresAt, 0))
		if err != nil || !consumed {
			return errDatasourceLinkStateRejected
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	exchange := l.s.newOAuth2AuthorizationCode
	if exchange == nil {
		exchange = apisource.AuthorizationCode
	}
	token, err := exchange(ctx, apisource.OAuth2Config{TokenURL: l.descriptor.TokenURL, ClientID: l.descriptor.ClientID, Scopes: l.descriptor.Scopes}, l.descriptor.ClientSecret, code, l.s.oauthVerifier(state.state), claims.RedirectURI)
	if err != nil {
		return "", "", err
	}
	if token == nil || token.RefreshToken == "" || token.AccessToken == "" {
		return "", "", errors.New("provider did not grant offline access")
	}
	ttl := token.ExpiresIn
	if ttl <= 0 {
		ttl = oauth2DefaultTTL
	}
	blob, _ := json.Marshal(oauthStoredCredential{RefreshToken: token.RefreshToken, ClientSecret: l.descriptor.ClientSecret, AccessToken: token.AccessToken, ExpiresAt: time.Now().Add(ttl).Unix()})
	if l.s.datasourceCipher == nil {
		return "", "", errors.New("source cipher unavailable")
	}
	ref, err := l.s.datasourceCipher.EncryptSecret(ctx, DatasourceConnectorSecretPurpose(claims.SourceID), string(blob))
	if err != nil {
		return "", "", errors.New("could not seal provider authorization")
	}
	if state.credentialRef == nil {
		return "", "", errDatasourceLinkStateRejected
	}
	*state.credentialRef = ref
	// OAuth alone does not attest a provider subject. This is a source-scoped
	// authorization identity, never an email match or a translated provider ACL.
	return "source:" + claims.SourceID, "Connected API", nil
}

// GetDatasourceSourceForActor applies the personal-source ceiling to management
// reads. The transport still enforces current organization membership.
func (s *Service) GetDatasourceSourceForActor(ctx context.Context, org, source, actor string) (*DatasourceSource, error) {
	src, err := s.GetDatasourceSource(ctx, org, source)
	if err != nil {
		return nil, err
	}
	if src.PersonalOwnerUserID != "" && src.PersonalOwnerUserID != actor {
		return nil, status.Error(codes.PermissionDenied, "source is personal to another member")
	}
	return src, nil
}

// oauthCredentialUnavailable deliberately discards lower-layer errors: a token
// exchange or cipher failure can contain secret-bearing URLs or plaintext.
func oauthCredentialUnavailable() error {
	return datasourceStatus(codes.Unavailable, "SOURCE_OAUTH_UNAVAILABLE", "source OAuth authorization unavailable", nil, 0)
}
