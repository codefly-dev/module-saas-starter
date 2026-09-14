package adapters

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"accounts/pkg/business"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

type installerHTTPService struct {
	moduleInstallationService
	calls int
}

func (s *installerHTTPService) ReconcileModuleInstallation(_ context.Context, c business.ModuleCaller, p *business.InstallerPolicy, r business.ModuleInstallationRequest, apply bool) (*business.ModuleInstallationResult, error) {
	if _, err := p.Authorize(c, r, time.Now()); err != nil {
		return nil, err
	}
	s.calls++
	return &business.ModuleInstallationResult{State: "ready", OrganizationID: c.BoundOrg}, nil
}
func TestInstallerHTTPRejectsExpiredForeignAndWrongAudienceCapabilities(t *testing.T) {
	policy, caller, request := installerPolicyFixture()
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "policy.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	resetModuleWorkContextAuthority(t)
	WorkContextSingleton().Configure(WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "installer-key", PrivateKey: private, Authority: &workContextAuthorityFake{}})
	rawHandler, err := NewModuleInstallationHTTPHandler(nil, path)
	require.NoError(t, err)
	h := rawHandler.(*ModuleInstallationHTTPHandler)
	svc := &installerHTTPService{}
	h.service = svc
	token, _, err := WorkContextSingleton().StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: caller.PrincipalID, Tenant: caller.BoundOrg})
	require.NoError(t, err)
	body, err := json.Marshal(request)
	require.NoError(t, err)
	post := func(token string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, moduleInstallationPrefix+"inspect", bytes.NewReader(body))
		req.Header.Set(codefly.WorkContextHeaderName, token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, want, w.Code, w.Body.String())
	}
	post(token.Encoded(), 200)
	require.Equal(t, 1, svc.calls)
	verifier, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{PublicKeys: map[string]ed25519.PublicKey{"installer-key": public}, Now: func() time.Time { return time.Now().Add(16 * time.Minute) }})
	require.NoError(t, err)
	WorkContextSingleton().verifier = verifier
	post(token.Encoded(), 401)
	require.Equal(t, 1, svc.calls)
	installModuleWorkContextAuthority(t)
	post(token.Encoded(), 401)
	post("not-a-token", 401)
	// A signed capability for another consumer is not a module capability.
	authority := WorkContextSingleton()
	moduleToken, _, err := authority.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: caller.PrincipalID, Tenant: caller.BoundOrg})
	require.NoError(t, err)
	wrong, _, err := authority.signer.ExchangeWorkContextAudience(moduleToken, codefly.ExchangeWorkContextAudienceInput{Audience: "other-consumer"})
	require.NoError(t, err)
	post(wrong.Encoded(), 401)
	require.Equal(t, 1, svc.calls)
}
func TestInstallerHTTPUnknownFieldsCannotRequestOwnerOrAuthority(t *testing.T) {
	policy, caller, _ := installerPolicyFixture()
	h := &ModuleInstallationHTTPHandler{policy: func() (*business.InstallerPolicy, error) { return policy, nil }, verify: func(string) (business.ModuleCaller, error) { return caller, nil }}
	for _, body := range []string{`{"moduleId":"acme.example/solution","ownerPrincipalId":"another"}`, `{} {}`, `{"prefix":"example-installer","secret":"x","organizationId":"another"}`} {
		mode := "inspect"
		if bytes.Contains([]byte(body), []byte("secret")) {
			mode = "token"
		}
		req := httptest.NewRequest(http.MethodPost, moduleInstallationPrefix+mode, bytes.NewBufferString(body))
		req.Header.Set(codefly.WorkContextHeaderName, "a-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code)
	}
}
