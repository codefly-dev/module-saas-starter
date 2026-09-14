//go:build !pure

package infra_test

import (
	"accounts/pkg/adapters"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func boundedInstallationFixture(t *testing.T) *business.InstallSolutionParams {
	org, owner, role, _, _ := installFixture(t, "documents", "read")
	return &business.InstallSolutionParams{OrgID: org, OwnerPrincipalID: owner, GrantedBy: owner, RoleID: role, AgentIdentifier: "acme.example/repeatable:1.0.0", SolutionIdentifier: "repeatable", AllowedAudiences: []string{"example.api"}, AllowedScopes: []string{"documents"}, InstallerPrincipalID: business.ModulePrincipalID("example-installer")}
}
func boundedReconcile(p *business.InstallSolutionParams, apply bool) (out *business.ModuleInstallationResult, err error) {
	err = testStore.WithOrgTx(testCtx, p.OrgID, func(ctx context.Context) error {
		var err error
		out, err = testStore.ReconcileModuleInstallation(ctx, p, []string{"documents:read"}, apply)
		return err
	})
	return
}
func TestModuleInstallationPostgresRepeatConcurrentAndLostResponse(t *testing.T) {
	p := boundedInstallationFixture(t)
	plan, err := boundedReconcile(p, false)
	require.NoError(t, err)
	require.Equal(t, "absent", plan.State)
	var wg sync.WaitGroup
	results := make(chan *business.ModuleInstallationResult, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() { out, err := boundedReconcile(p, true); results <- out; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	firstID := ""
	changes := 0
	for out := range results {
		if firstID == "" {
			firstID = out.InstallationID
		}
		require.Equal(t, firstID, out.InstallationID)
		require.NotEmpty(t, out.GrantID)
		if out.Changed {
			changes++
		}
	}
	require.Equal(t, 1, changes)
	// Simulate a committed HTTP response lost to the caller: ignore it, then
	// inspect and apply from authoritative database state without a local receipt.
	observed, err := boundedReconcile(p, false)
	require.NoError(t, err)
	require.Equal(t, firstID, observed.InstallationID)
	repeat, err := boundedReconcile(p, true)
	require.NoError(t, err)
	require.Equal(t, observed, repeat)
	require.NoError(t, testStore.WithOrgTx(testCtx, p.OrgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE org_id=$1 AND agent_identifier=$2`, p.OrgID, p.AgentIdentifier).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("agent count=%d", n)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM scope_grants WHERE org_id=$1 AND subject_id=$2`, p.OrgID, repeat.PrincipalID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("grant count=%d", n)
		}
		return nil
	}))
}
func TestModuleInstallationPostgresRollbackAndConflicts(t *testing.T) {
	p := boundedInstallationFixture(t)
	stopped := errors.New("terminated before transaction commit")
	err := testStore.WithOrgTx(testCtx, p.OrgID, func(ctx context.Context) error {
		_, err := testStore.ReconcileModuleInstallation(ctx, p, []string{"documents:read"}, true)
		if err != nil {
			return err
		}
		return stopped
	})
	require.ErrorIs(t, err, stopped)
	plan, err := boundedReconcile(p, false)
	require.NoError(t, err)
	require.Equal(t, "absent", plan.State)
	created, err := boundedReconcile(p, true)
	require.NoError(t, err)
	p.AgentIdentifier = "acme.example/repeatable:2.0.0"
	_, err = boundedReconcile(p, true)
	require.Error(t, err)
	p.AgentIdentifier = "acme.example/repeatable:1.0.0"
	p.InstallerPrincipalID = business.ModulePrincipalID("another-installer")
	_, err = boundedReconcile(p, true)
	require.Error(t, err)
	p.InstallerPrincipalID = business.ModulePrincipalID("example-installer")
	require.NoError(t, testStore.WithOrgTx(testCtx, p.OrgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx)
		_, err := tx.Exec(ctx, `INSERT INTO role_permissions(role_id,resource,action) VALUES($1,'documents','write')`, p.RoleID)
		return err
	})) //nolint:staticcheck
	_, err = boundedReconcile(p, true)
	require.ErrorContains(t, err, "permissions have changed")
	require.NotEmpty(t, created.InstallationID)
}
func TestModuleInstallationPostgresDoesNotAdoptHumanInstallation(t *testing.T) {
	p := boundedInstallationFixture(t)
	p.InstallerPrincipalID = ""
	require.NoError(t, testStore.WithOrgTx(testCtx, p.OrgID, func(ctx context.Context) error { _, err := testStore.InstallSolution(ctx, p); return err }))
	p.InstallerPrincipalID = business.ModulePrincipalID("example-installer")
	_, err := boundedReconcile(p, true)
	require.ErrorContains(t, err, "automatic adoption is forbidden")
}

func TestModuleInstallationPostgresHTTPAuthenticationAndAudit(t *testing.T) {
	p := boundedInstallationFixture(t)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	svc := auditedService(t, emitter)
	svc.SetModuleIdentitySecrets(map[string][sha256.Size]byte{"example-installer": sha256.Sum256([]byte("local-fixture-secret"))})
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{p.InstallerPrincipalID: {Prefix: "example-installer", Tenant: p.OrgID}})
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	adapters.WorkContextSingleton().Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "installer-test", PrivateKey: private, Authority: testStore})
	policy := business.InstallerPolicy{Version: "accounts.module-installation-policy/v1", Delegations: []business.InstallerDelegation{{Prefix: "example-installer", OrganizationID: p.OrgID, ModuleID: "acme.example/repeatable", AgentIdentifiers: []string{p.AgentIdentifier}, SolutionIdentifier: p.SolutionIdentifier, RoleID: p.RoleID, RolePermissions: []string{"documents:read"}, AllowedAudiences: p.AllowedAudiences, AllowedScopes: p.AllowedScopes, OwnerPrincipalID: p.OwnerPrincipalID, ExpiresAt: time.Now().Add(time.Hour)}}}
	path := filepath.Join(t.TempDir(), "policy.json")
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	handler, err := adapters.NewModuleInstallationHTTPHandler(svc, path)
	require.NoError(t, err)
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	post := func(mode string, body any, token string, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/module-installations/"+mode, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Codefly-Work-Context", token)
		}
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err = io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, want, response.StatusCode, string(raw))
		return raw
	}
	post("token", map[string]string{"prefix": "example-installer", "secret": "wrong"}, "", 403)
	raw = post("token", map[string]string{"prefix": "example-installer", "secret": "local-fixture-secret"}, "", 200)
	var issued struct {
		Token     string
		ExpiresAt time.Time
	}
	require.NoError(t, json.Unmarshal(raw, &issued))
	require.NotEmpty(t, issued.Token)
	require.WithinDuration(t, time.Now().Add(15*time.Minute), issued.ExpiresAt, 5*time.Second)
	request := business.ModuleInstallationRequest{DisplayName: "Example Repeatable", RootScopeLabel: "Example Repeatable", ModuleID: "acme.example/repeatable", OrganizationSlug: "org-" + p.OrgID, AgentIdentifier: p.AgentIdentifier, SolutionIdentifier: p.SolutionIdentifier, RoleID: p.RoleID, ExpectedRolePermissions: []string{"documents:read"}, AllowedAudiences: p.AllowedAudiences, AllowedScopes: p.AllowedScopes}

	if script := os.Getenv("MODULE_INSTALLER_CLIENT_SCRIPT"); script != "" {
		dir := t.TempDir()
		caFile, credentialFile, scenarioFile := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "credential"), filepath.Join(dir, "scenario.json")
		require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
		require.NoError(t, os.WriteFile(credentialFile, []byte("local-fixture-secret"), 0600))
		scenario := map[string]any{"accounts_url": server.URL, "ca_file": caFile, "credential_file": credentialFile, "installer_prefix": "example-installer", "organization_slug": request.OrganizationSlug, "organization_id": p.OrgID, "role_id": p.RoleID, "expected_role_permissions": request.ExpectedRolePermissions, "module_id": request.ModuleID, "agent_identifier": request.AgentIdentifier, "solution_identifier": request.SolutionIdentifier, "allowed_audiences": request.AllowedAudiences, "allowed_scopes": request.AllowedScopes, "display_name": request.DisplayName, "root_scope_label": request.RootScopeLabel}
		raw, err := json.Marshal(scenario)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(scenarioFile, raw, 0600))
		command := exec.Command("python3", script, scenarioFile)
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		t.Log(string(output))
		// The external client intentionally installed this same immutable row.
		// Subsequent local API checks below must observe its committed result.
	}
	post("inspect", request, "", 401)
	post("inspect", request, issued.Token, 200)
	if os.Getenv("MODULE_INSTALLER_CLIENT_SCRIPT") == "" {
		post("verify", request, issued.Token, 409)
	}
	raw = post("apply", request, issued.Token, 200)
	var result business.ModuleInstallationResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Equal(t, os.Getenv("MODULE_INSTALLER_CLIENT_SCRIPT") == "", result.Changed)
	raw = post("apply", request, issued.Token, 200)
	var repeat business.ModuleInstallationResult
	require.NoError(t, json.Unmarshal(raw, &repeat))
	require.False(t, repeat.Changed)
	require.Equal(t, result.InstallationID, repeat.InstallationID)
	post("verify", request, issued.Token, 200)
	require.Equal(t, 1, countAuditEvents(t, string(business.EventInstallationCreated), result.InstallationID))
	request.OrganizationSlug = "another-org"
	post("apply", request, issued.Token, 403)
	request.OrganizationSlug = "org-" + p.OrgID
	// Policy removal invalidates already issued tokens without waiting for their
	// 15-minute lifetime, and also refuses replacement credentials.
	policy.Delegations = nil
	raw, err = json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	post("apply", request, issued.Token, 403)
	post("token", map[string]string{"prefix": "example-installer", "secret": "local-fixture-secret"}, "", 403)
	require.NoError(t, os.Remove(path))
	post("inspect", request, issued.Token, 503)
}

func TestModuleInstallationPostgresRejectsGrantDriftAndIneligibleOwner(t *testing.T) {
	t.Run("unrelated grant", func(t *testing.T) {
		p := boundedInstallationFixture(t)
		created, err := boundedReconcile(p, true)
		require.NoError(t, err)
		require.NoError(t, testStore.WithOrgTx(testCtx, p.OrgID, func(ctx context.Context) error {
			tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck
			_, err := tx.Exec(ctx, `INSERT INTO scope_grants(id,org_id,subject_id,subject_kind,scope_path,role_id,granted_by) SELECT gen_random_uuid(),$1,$2,'principal',n.scope_path,$3,$4 FROM scope_nodes n WHERE n.org_id=$1 AND n.id <> $5 LIMIT 1`, p.OrgID, created.PrincipalID, p.RoleID, p.GrantedBy, created.ScopeNodeID)
			return err
		}))
		_, err = boundedReconcile(p, false)
		require.ErrorContains(t, err, "unrelated grants require review")
	})
	t.Run("suspended owner", func(t *testing.T) {
		p := boundedInstallationFixture(t)
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			tx := ctx.Value("tx").(pgx.Tx)
			_, err := tx.Exec(ctx, `UPDATE users SET status='suspended' WHERE uuid=$1`, p.OwnerPrincipalID)
			return err
		})) //nolint:staticcheck
		_, err := boundedReconcile(p, true)
		require.ErrorContains(t, err, "no longer an organization administrator")
	})
}
