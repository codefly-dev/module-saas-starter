package business

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"accounts/pkg/datasource/apisource"
)

func TestOAuthPKCEIsNotInState(t *testing.T) {
	s := &Service{datasourceLinkKey: []byte("local-example-signing-key")}
	l := oauthAccountLinker{s: s, descriptor: DatasourceOAuthProvider{AuthorizeURL: "https://auth.example.com/authorize", ClientID: "example", Scopes: []string{"read"}}}
	state, err := s.signDatasourceLinkState(datasourceLinkClaims{OrgID: "org", UserID: "person", Connector: "api", Nonce: "nonce", SourceID: "source", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(l.AuthorizeURL(state, "https://host.example.com/callback"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	challenge := sha256.Sum256([]byte(s.oauthVerifier(state)))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("state") != state {
		t.Fatal("invalid PKCE authorization request")
	}
	if q.Get("code_verifier") != "" {
		t.Fatal("verifier disclosed")
	}
	if s.oauthVerifier(state) == s.oauthVerifier(state+"changed") {
		t.Fatal("verifier not bound to state")
	}
}
func TestClientCredentialsEnvelopeHasNoRefreshToken(t *testing.T) {
	source := &DatasourceSource{API: &APIDatasourceConfig{CredentialKind: APICredentialKindOAuth2, OAuth2: &APIOAuth2Config{Grant: OAuth2ClientCredentials}}}
	plaintext, err := connectorCredentialPlaintext(source, "client-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	var stored oauthStoredCredential
	if err := json.Unmarshal([]byte(plaintext), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "" || stored.ClientSecret != "client-secret" {
		t.Fatal("wrong credential custody")
	}
}

// The generic linker fails closed before exchanging a code without its host
// supplied, verified state context.
func TestOAuthLinkerRefusesUnboundCode(t *testing.T) {
	calls := 0
	s := &Service{newOAuth2AuthorizationCode: func(context.Context, apisource.OAuth2Config, string, string, string, string) (*apisource.OAuth2Token, error) {
		calls++
		return nil, nil
	}}
	l := oauthAccountLinker{s: s}
	if _, _, err := l.ResolveAccount(t.Context(), "code"); err == nil || calls != 0 {
		t.Fatal("unbound code exchanged")
	}
}

func TestAPICredentialBudgetIsSharedAndOpaque(t *testing.T) {
	s := &Service{datasourceLinkKey: []byte("example-deployment-key")}
	a := &APIDatasourceConfig{BaseURL: "https://api.example.com/a", CredentialKind: APICredentialKindBearer}
	b := &APIDatasourceConfig{BaseURL: "https://api.example.com/b", CredentialKind: APICredentialKindBearer}
	first := s.apiCredentialBudgetKey(a, "example-secret")
	if first == "" || first != s.apiCredentialBudgetKey(b, "example-secret") {
		t.Fatal("same credential does not share budget")
	}
	if first == s.apiCredentialBudgetKey(b, "different-secret") {
		t.Fatal("different credential shares budget")
	}
	s.datasourceLinkKey = []byte("another-deployment-key")
	if first == s.apiCredentialBudgetKey(a, "example-secret") {
		t.Fatal("credential key is not protected by deployment key")
	}
}

func TestOAuthRegistrationCannotShadowConnectorLinker(t *testing.T) {
	s := &Service{}
	err := s.ConfigureDatasourceOAuth(`{"github":{"authorize_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token"}}`, `{"github":"example-client"}`, `{}`)
	if err == nil {
		t.Fatal("generic OAuth registration replaced a connector-owned linker")
	}
	if len(s.datasourceOAuth) != 0 {
		t.Fatal("invalid registration was installed")
	}
	if err := s.ConfigureDatasourceOAuth(`{"api":{"authorize_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token"}}`, `{"api":"example-client"}`, `{}`); err != nil {
		t.Fatal(err)
	}
}
