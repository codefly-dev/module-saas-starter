package main

import (
	"accounts/pkg/adapters"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"context"
	cryptokey "crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// Only persistence is a test double. The configured host, TLS listener, policy
// loader, identity-secret comparison, signer, verifier and handlers are real.
// Durable installation/audit transaction semantics have separate DB tests.
type installerListenerStore struct {
	business.Store
	mu    sync.Mutex
	calls []bool
}

const installerListenerOrg = "11111111-1111-4111-8111-111111111111"

func (s *installerListenerStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *installerListenerStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *installerListenerStore) OrganizationIDExists(context.Context, string) (bool, error) {
	return true, nil
}
func (s *installerListenerStore) GetOrganizationBySlug(context.Context, string) (*gen.Organization, error) {
	return &gen.Organization{Id: installerListenerOrg}, nil
}
func (s *installerListenerStore) ReconcileModuleInstallation(_ context.Context, _ *business.InstallSolutionParams, _ []string, apply bool) (*business.ModuleInstallationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, apply)
	return &business.ModuleInstallationResult{State: "ready", OrganizationID: installerListenerOrg}, nil
}

func (s *installerListenerStore) observed() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.calls...)
}

type installerListenerFixture struct {
	client     *http.Client
	origin     string
	policyPath string
	store      *installerListenerStore
	request    business.ModuleInstallationRequest
}

func newInstallerListener(t *testing.T, enabled bool) installerListenerFixture {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0600))
		return p
	}
	_, key, err := cryptokey.GenerateKey(rand.Reader)
	require.NoError(t, err)
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, key.Public(), key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(certPEM))
	config := executionCustodyProjection{TLSCertFile: write("cert", certPEM), TLSKeyFile: write("key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})), ClientCAFile: write("ca", certPEM), Consumers: map[string]adapters.ExecutionConsumerPolicy{"example": {WorkerURI: "spiffe://example.test/worker", ParentAudience: "example.facade", TaskAudience: "example.tasks", Audience: "example.model", Profile: "example-profile@1", ResourceKind: "example.model", ResourceID: "example-profile", InvokeAction: "invoke", ReadAction: "read", TaskResourceKind: "example.tasks", TaskActions: []string{"execute", "read", "start"}}}}
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", write("custody.json", raw))
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "verified-tls")
	t.Setenv("CODEFLY_INTERNAL_TOKEN", strings.Repeat("i", 48))
	jwt := ed25519minter.New(ed25519minter.Config{Issuer: "example.accounts", Audience: "example.accounts"}, key, nil)
	host, err := configuredExecutionCustody(nil, infra.NewVaultClientDirect("https://example.invalid", ""), jwt, jwt.KeyID(), key, true, false)
	require.NoError(t, err)
	singleton := adapters.WorkContextSingleton()
	previous := *singleton
	t.Cleanup(func() { *singleton = previous })
	// The listener cases never resolve ordinary user/custody authority. The
	// concrete store's uncalled interface supplies only normal signer setup.
	singleton.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "example.accounts", KeyID: jwt.KeyID(), PrivateKey: key, Authority: (*infra.PostgresStore)(nil)})
	store := &installerListenerStore{}
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetModuleIdentitySecrets(map[string][sha256.Size]byte{"example-installer": sha256.Sum256([]byte("example-identity-secret"))})
	service.SetModulePrincipals(business.ModulePrincipalRegistry{business.ModulePrincipalID("example-installer"): {Prefix: "example-installer", Tenant: installerListenerOrg}})
	delegation := business.InstallerDelegation{Prefix: "example-installer", OrganizationID: installerListenerOrg, ModuleID: "acme.example/solution", AgentIdentifiers: []string{"acme.example/solution:1.0.0"}, SolutionIdentifier: "example-solution", RoleID: "22222222-2222-4222-8222-222222222222", RolePermissions: []string{"example.records:read"}, AllowedAudiences: []string{"example.api"}, AllowedScopes: []string{"example.records"}, OwnerPrincipalID: "33333333-3333-4333-8333-333333333333", ExpiresAt: time.Now().Add(time.Hour)}
	raw, err = json.Marshal(business.InstallerPolicy{Version: "accounts.module-installation-policy/v1", Delegations: []business.InstallerDelegation{delegation}})
	require.NoError(t, err)
	policyPath := write("policy.json", raw)
	t.Setenv("MODULE_INSTALLER_POLICY_FILE", "")
	if enabled {
		t.Setenv("MODULE_INSTALLER_POLICY_FILE", policyPath)
	}
	handler, err := configuredModuleInstaller(service)
	require.NoError(t, err)
	if !enabled {
		require.Nil(t, handler)
	}
	mountExecutionInstaller(host, handler)
	addresses := map[string]string{}
	stop, err := startExecutionCustodyWithListener(host, func(name string) (net.Listener, error) {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e == nil {
			addresses[name] = l.Addr().String()
		}
		return l, e
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	return installerListenerFixture{client: &http.Client{Transport: transport, Timeout: 2 * time.Second}, origin: "https://" + addresses["custody"], policyPath: policyPath, store: store, request: business.ModuleInstallationRequest{ModuleID: delegation.ModuleID, OrganizationSlug: "example-org", AgentIdentifier: delegation.AgentIdentifiers[0], SolutionIdentifier: delegation.SolutionIdentifier, RoleID: delegation.RoleID, ExpectedRolePermissions: delegation.RolePermissions, AllowedAudiences: delegation.AllowedAudiences, AllowedScopes: delegation.AllowedScopes}}
}

func (f installerListenerFixture) call(t *testing.T, method, path, body string, tokens ...string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.origin+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for _, token := range tokens {
		req.Header.Add(codefly.WorkContextHeaderName, token)
	}
	response, err := f.client.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	require.NoError(t, err)
	return response.StatusCode, data
}
func (f installerListenerFixture) token(t *testing.T) string {
	t.Helper()
	status, raw := f.call(t, "POST", "/v1/module-installations/token", `{"prefix":"example-installer","secret":"example-identity-secret"}`)
	require.Equal(t, 200, status, string(raw))
	var result struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.NotEmpty(t, result.Token)
	return result.Token
}

func TestExecutionInstallerTLSRoutesAndAuthentication(t *testing.T) {
	f := newInstallerListener(t, true)
	token := f.token(t)
	raw, err := json.Marshal(f.request)
	require.NoError(t, err)
	for _, mode := range []string{"inspect", "apply", "verify"} {
		path := "/v1/module-installations/" + mode
		for _, tokens := range [][]string{nil, {"forged"}, {token, token}} {
			status, _ := f.call(t, "POST", path, string(raw), tokens...)
			require.Equal(t, 401, status)
		}
		status, body := f.call(t, "POST", path, string(raw), token)
		require.Equal(t, 200, status, string(body))
	}
	require.Equal(t, []bool{false, true, false}, f.store.observed())
	for _, body := range []string{`{"prefix":"example-installer","secret":"wrong"}`, `{"prefix":"unknown","secret":"example-identity-secret"}`} {
		status, _ := f.call(t, "POST", "/v1/module-installations/token", body)
		require.Equal(t, 403, status)
	}
	f.request.AllowedScopes = []string{"*"}
	raw, err = json.Marshal(f.request)
	require.NoError(t, err)
	status, _ := f.call(t, "POST", "/v1/module-installations/apply", string(raw), token)
	require.Equal(t, 403, status)
	require.Len(t, f.store.observed(), 3)
	require.NoError(t, os.WriteFile(f.policyPath, []byte(`{"version":"accounts.module-installation-policy/v1","delegations":[]}`), 0600))
	status, _ = f.call(t, "POST", "/v1/module-installations/inspect", string(raw), token)
	require.Equal(t, 403, status)
	require.NoError(t, os.Remove(f.policyPath))
	status, _ = f.call(t, "POST", "/v1/module-installations/inspect", string(raw), token)
	require.Equal(t, 503, status)
	require.Len(t, f.store.observed(), 3)
}

func TestExecutionInstallerTLSExactPathsAndExistingRoutes(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			f := newInstallerListener(t, enabled)
			for _, suffix := range []string{"token", "inspect", "apply", "verify"} {
				status, _ := f.call(t, "GET", "/v1/module-installations/"+suffix, "")
				if enabled {
					require.Equal(t, 405, status)
				} else {
					require.Equal(t, 400, status)
				}
				if !enabled {
					status, _ = f.call(t, "POST", "/v1/module-installations/"+suffix, "{}")
					require.Equal(t, 404, status)
				}
			}
			for _, path := range []string{"/v1/module-installations/", "/v1/module-installations/token/", "/v1/module-installations/token/extra", "/v1/module-installations/Token", "/v1/status", "/v1/billing/checkout", "/v1/auth/.well-known/jwks.json/extra"} {
				status, _ := f.call(t, "POST", path, "{}")
				require.Equal(t, 404, status, path)
			}
			for _, path := range []string{"/private/v1/execution-custody/register", "/private/v1/execution-custody/recover", "/private/v1/execution-custody/exchange"} {
				status, _ := f.call(t, "POST", path, "{}")
				require.Equal(t, 401, status, path)
			}
			status, body := f.call(t, "GET", "/v1/auth/.well-known/jwks.json", "")
			require.Equal(t, 200, status)
			require.Contains(t, string(body), `"keys"`)
		})
	}
}

func TestExecutionInstallerPolicyConfiguration(t *testing.T) {
	t.Setenv("MODULE_INSTALLER_POLICY_FILE", filepath.Join(t.TempDir(), "missing"))
	handler, err := configuredModuleInstaller(nil)
	require.Error(t, err)
	require.Nil(t, handler)
	path := filepath.Join(t.TempDir(), "invalid.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":"unknown"}`), 0600))
	t.Setenv("MODULE_INSTALLER_POLICY_FILE", path)
	_, err = configuredModuleInstaller(nil)
	require.Error(t, err)
	// Existing deployments without a custody listener retain their REST path.
	require.NotPanics(t, func() { mountExecutionInstaller(nil, http.NotFoundHandler()) })
}
