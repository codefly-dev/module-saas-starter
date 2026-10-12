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
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	resetModuleWorkContextAuthority(t)
	WorkContextSingleton().Configure(WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "installer-key", PrivateKey: private, Authority: &workContextAuthorityFake{},
		Seals: newSealAnswersFake(12)})
	rawHandler, err := NewModuleInstallationHTTPHandler(nil, path)
	require.NoError(t, err)
	h := rawHandler.(*ModuleInstallationHTTPHandler)
	svc := &installerHTTPService{}
	h.service = svc
	token := mintModuleIdentity(t, caller.PrincipalID, caller.BoundOrg)
	body, err := json.Marshal(request)
	require.NoError(t, err)
	post := func(token string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, moduleInstallationPrefix+"inspect", bytes.NewReader(body))
		req.Header.Set(workContextHeader, token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, want, w.Code, w.Body.String())
	}
	post(token, 200)
	require.Equal(t, 1, svc.calls)
	// Past its window.
	WorkContextSingleton().now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	post(token, 401)
	WorkContextSingleton().now = nil
	require.Equal(t, 1, svc.calls)
	// Under a key this issuer no longer holds.
	installModuleWorkContextAuthority(t)
	post(token, 401)
	post("not-a-token", 401)
	// A signed capability for another consumer is not a module capability.
	build := testBuildFor(caller.PrincipalID)
	workContextSingleton.seals.(*sealAnswersFake).approve(caller.PrincipalID, build)
	wrong, _ := mintTestCapability(t, WorkContextSingleton(), testCapability{
		Audience: "other-consumer", Tenant: caller.BoundOrg, Owner: caller.PrincipalID,
		OwnerKind: business.PrincipalKindService, Execution: build,
	})
	post(wrong, 401)
	require.Equal(t, 1, svc.calls)
}
func TestInstallerHTTPUnknownFieldsCannotRequestOwnerOrAuthority(t *testing.T) {
	policy, caller, _ := installerPolicyFixture()
	h := &ModuleInstallationHTTPHandler{policy: func() (*business.InstallerPolicy, error) { return policy, nil }, verify: func(context.Context, string) (business.ModuleCaller, error) { return caller, nil }}
	for _, body := range []string{`{"moduleId":"acme.example/solution","ownerPrincipalId":"another"}`, `{} {}`, `{"prefix":"example-installer","secret":"x","organizationId":"another"}`} {
		mode := "inspect"
		if bytes.Contains([]byte(body), []byte("secret")) {
			mode = "token"
		}
		req := httptest.NewRequest(http.MethodPost, moduleInstallationPrefix+mode, bytes.NewBufferString(body))
		req.Header.Set(workContextHeader, "a-token")
		// The token route reads its credential from Authorization (#1056), so the
		// body is decoded — and its unknown fields refused — only past it.
		req.Header.Set("Authorization", "Bearer projected-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code)
	}
}
