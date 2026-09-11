package adapters

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/auth"
	minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	wire "accounts/pkg/executioncustody"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	base "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

func custodyTLS(t *testing.T) (*tls.Config, *tls.Config, *tls.Config, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Example local CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	root, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	cert := func(n int64, worker string) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		c := &x509.Certificate{SerialNumber: big.NewInt(n), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		if worker != "" {
			u, err := url.Parse(worker)
			require.NoError(t, err)
			c.URIs = []*url.URL{u}
		}
		der, err := x509.CreateCertificate(rand.Reader, c, root, &key.PublicKey, caKey)
		require.NoError(t, err)
		pk, err := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, err)
		pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}))
		require.NoError(t, err)
		return pair
	}
	return &tls.Config{Certificates: []tls.Certificate{cert(2, "")}, ClientCAs: pool}, &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert(3, "spiffe://example.test/worker")}}, &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert(4, "spiffe://example.test/other")}}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestExecutionCustodyReal(t *testing.T) {
	if os.Getenv("CUSTODY_TEST_ADMIN") == "" {
		t.Skip("run qualification/execution-custody/run.py for real PostgreSQL and Vault")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, os.Getenv("CUSTODY_TEST_ADMIN"))
	require.NoError(t, err)
	defer admin.Close()
	sql := func(query string, args ...any) {
		t.Helper()
		_, err := admin.Exec(ctx, query, args...)
		require.NoError(t, err)
	}
	owner, org, actor, role := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	taskID, sessionID := uuid.NewString(), uuid.NewString()
	sql(`INSERT INTO users(uuid,primary_email,status) VALUES($1,'user@example.com','active')`, owner)
	sql(`INSERT INTO organizations(id,name,slug,owner_id) VALUES($1,'Example','example',$2)`, org, owner)
	sql(`INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'owner')`, org, owner)
	sql(`INSERT INTO principals(id,kind,display_name) VALUES($1,'human','Example owner') ON CONFLICT DO NOTHING`, owner)
	sql(`INSERT INTO principals(id,kind,display_name,org_id,agent_identifier,allowed_audiences,allowed_scopes) VALUES($1,'agent','Example agent',$2,'example.test/agent:1',ARRAY['example.facade','example.tasks','example.model'],ARRAY['example.tasks','example.model'])`, actor, org)
	sql(`INSERT INTO roles(id,name,org_id) VALUES($1,'example-execution',$2)`, role, org)
	for _, scope := range []struct{ kind, action string }{{"example.tasks", "start"}, {"example.tasks", "execute"}, {"example.tasks", "read"}, {"example.model", "invoke"}, {"example.model", "read"}} {
		sql(`INSERT INTO role_permissions(role_id,resource,action) VALUES($1,$2,$3)`, role, scope.kind, scope.action)
	}
	sql(`INSERT INTO role_assignments(subject_id,subject_kind,role_id,org_id) VALUES($1,'principal',$2,$3)`, actor, role, org)
	openStore := func() *infra.PostgresStore {
		s, err := infra.NewPostgresStoreWithCapabilities(ctx, os.Getenv("CUSTODY_TEST_READER"), os.Getenv("CUSTODY_TEST_WRITER"))
		require.NoError(t, err)
		return s
	}
	store := openStore()
	defer func() { store.Close() }()
	svc, err := business.NewService(store)
	require.NoError(t, err)
	oldService := service
	WithService(svc)
	defer WithService(oldService)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	jwt := minter.New(minter.Config{Issuer: "example.accounts", Audience: "example.accounts"}, key, pgauth.NewSessionStore(store))
	identity := &auth.Identity{UserID: uuid.MustParse(owner), OrgID: uuid.MustParse(org), OrgRole: "owner"}
	pair, err := jwt.Mint(ctx, identity)
	require.NoError(t, err)
	authority := &WorkContextAuthorityServer{}
	authority.Configure(WorkContextAuthorityConfiguration{Issuer: "example.work", KeyID: "example-key", PrivateKey: key, Authority: store})
	ownerCtx := stampRequestIdentity(ctx, auth.RequestIdentityOf(identity), identity.Assurance())
	start := &gen.StartTaskWorkContextRequest{OrgId: org, TaskId: taskID, SessionId: sessionID, Audience: "example.facade", ActorPrincipalId: actor, TtlSeconds: 600, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example.tasks", ResourceIds: []string{taskID}, Actions: []string{"execute", "read", "start"}}, {ResourceKind: "example.model", ResourceIds: []string{"example-profile"}, Actions: []string{"invoke", "read"}}}}
	issued, err := authority.StartTask(ownerCtx, start)
	require.NoError(t, err)
	parentToken, err := codefly.ParseWorkContextToken(issued.Token)
	require.NoError(t, err)
	parent, err := authority.verifier.Verify(parentToken, codefly.WorkContextExpectations{Issuer: authority.issuer})
	require.NoError(t, err)
	input := wire.RegisterRequest{Binding: wire.Binding{OrgID: org, OwnerID: owner, AdmissionID: strings.Repeat("a", 48), IntentDigest: strings.Repeat("b", 64), TaskID: parent.TaskId, SessionID: parent.SessionId, Consumer: "example", Profile: "example-profile@1"}, ParentToken: issued.Token, TaskExpiresAt: parent.ExpiresAtUnix - 20}
	policy := ExecutionConsumerPolicy{WorkerURI: "spiffe://example.test/worker", ParentAudience: "example.facade", TaskAudience: "example.tasks", Audience: "example.model", Profile: input.Binding.Profile, ResourceKind: "example.model", ResourceID: "example-profile", InvokeAction: "invoke", ReadAction: "read", TaskResourceKind: "example.tasks", TaskActions: []string{"execute", "read", "start"}}
	cipher := infra.NewVaultClientDirect(os.Getenv("CUSTODY_TEST_VAULT"), os.Getenv("CUSTODY_TEST_VAULT_TOKEN"))
	config := ExecutionCustodyConfig{Authority: authority, Minter: jwt, Store: store, Cipher: cipher, Consumers: map[string]ExecutionConsumerPolicy{"example": policy}}
	serverTLS, workerTLS, otherTLS, caPEM := custodyTLS(t)
	serve := func() (*httptest.Server, *wire.Client, *wire.Client, *wire.Client) {
		server, err := NewExecutionCustodyServer(config, serverTLS)
		require.NoError(t, err)
		ts := httptest.NewUnstartedServer(server.Handler)
		ts.TLS = server.TLSConfig
		ts.StartTLS()
		worker, err := wire.NewClient(ts.URL, &http.Transport{TLSClientConfig: workerTLS})
		require.NoError(t, err)
		other, err := wire.NewClient(ts.URL, &http.Transport{TLSClientConfig: otherTLS})
		require.NoError(t, err)
		ownerTLS := workerTLS.Clone()
		ownerTLS.Certificates = nil
		caller, err := wire.NewClient(ts.URL, &http.Transport{TLSClientConfig: ownerTLS})
		require.NoError(t, err)
		return ts, worker, other, caller
	}
	ts, worker, other, caller := serve()
	defer func() { ts.Close() }()
	registration, err := caller.Register(ctx, pair.AccessToken, input)
	require.NoError(t, err)
	exchange := wire.ExchangeRequest{Reference: registration.Reference, Binding: registration.Binding, Audience: policy.Audience}
	failure := func(t *testing.T, err error, code string) {
		t.Helper()
		var typed *wire.Error
		require.ErrorAs(t, err, &typed)
		require.Equal(t, code, typed.Code)
	}
	t.Run("lost_registration_ack_original_child", func(t *testing.T) {
		again, err := caller.Register(ctx, pair.AccessToken, input)
		require.NoError(t, err)
		require.True(t, registration == again, "original registration changed")
		// A fresh real owner access token is allowed; the original execution parent
		// remains byte-identical and its absolute horizon never moves.
		refreshed, err := jwt.Mint(ctx, identity)
		require.NoError(t, err)
		again, err = caller.Register(ctx, refreshed.AccessToken, input)
		require.NoError(t, err)
		require.True(t, registration == again, "original registration changed")
	})
	t.Run("concurrent_registration_converges", func(t *testing.T) {
		in := input
		in.Binding.AdmissionID = strings.Repeat("d", 48)
		var wg sync.WaitGroup
		results := make([]wire.Registration, 4)
		failures := make([]error, 4)
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); results[i], failures[i] = caller.Register(ctx, pair.AccessToken, in) }(i)
		}
		wg.Wait()
		for i := range results {
			require.NoError(t, failures[i])
			require.True(t, results[0] == results[i], "concurrent registration changed original child")
		}
	})
	t.Run("registration_tuple_substitution", func(t *testing.T) {
		for _, change := range []func(*wire.RegisterRequest){func(v *wire.RegisterRequest) { v.Binding.OwnerID = uuid.NewString() }, func(v *wire.RegisterRequest) { v.Binding.OrgID = uuid.NewString() }, func(v *wire.RegisterRequest) { v.Binding.TaskID = "other" }, func(v *wire.RegisterRequest) { v.Binding.SessionID = "other" }, func(v *wire.RegisterRequest) { v.TaskExpiresAt = parent.ExpiresAtUnix + 1 }} {
			in := input
			change(&in)
			_, err := caller.Register(ctx, pair.AccessToken, in)
			failure(t, err, "PermissionDenied")
		}
	})
	t.Run("stored_private_ciphertext_and_roles", func(t *testing.T) {
		var envelope string
		require.NoError(t, admin.QueryRow(ctx, `SELECT envelope FROM execution_custody WHERE reference=$1`, registration.Reference).Scan(&envelope))
		require.False(t, strings.Contains(envelope, input.ParentToken), "parent persisted in plaintext")
		require.False(t, strings.Contains(envelope, registration.TaskToken), "child persisted in plaintext")
		require.True(t, strings.HasPrefix(envelope, "cfs1:vault-transit:"))
		for _, role := range []string{"app_tenant", "app_job_worker", "app_webhook_worker", "custody_reader", "custody_writer"} {
			var allowed bool
			require.NoError(t, admin.QueryRow(ctx, `SELECT has_table_privilege($1,'execution_custody','SELECT')`, role).Scan(&allowed))
			require.False(t, allowed, role)
		}
		for _, column := range []string{"reference", "org_id", "owner_id", "admission_id", "fingerprint", "expires_at", "envelope"} {
			var canUpdate bool
			require.NoError(t, admin.QueryRow(ctx, `SELECT has_column_privilege('app_control_plane','execution_custody',$1,'UPDATE')`, column).Scan(&canUpdate))
			require.Equal(t, column == "envelope", canUpdate)
		}
		var enabled, forced bool
		require.NoError(t, admin.QueryRow(ctx, `SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid='execution_custody'::regclass`).Scan(&enabled, &forced))
		require.True(t, enabled && forced)

	})
	t.Run("revision_only_and_spoofed_owner_headers", func(t *testing.T) {
		_, err := caller.Register(ctx, "revision-only", input)
		failure(t, err, "Unauthenticated")
		body, _ := json.Marshal(input)
		req, err := http.NewRequest(http.MethodPost, ts.URL+wire.RegisterPath, strings.NewReader(string(body)))
		require.NoError(t, err)
		req.Header.Set("X-User-ID", owner)
		req.Header.Set("X-Org-ID", org)
		req.Header.Set("X-Codefly-Internal-Token", "revision-only")
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: workerTLS}}
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 401, resp.StatusCode)
		_, err = caller.Exchange(ctx, exchange)
		failure(t, err, "Unauthenticated")
	})
	t.Run("cross_worker_and_binding_substitutions", func(t *testing.T) {
		_, err := other.Exchange(ctx, exchange)
		failure(t, err, "PermissionDenied")
		changes := []func(*wire.ExchangeRequest){func(v *wire.ExchangeRequest) { v.Binding.OrgID = uuid.NewString() }, func(v *wire.ExchangeRequest) { v.Binding.OwnerID = uuid.NewString() }, func(v *wire.ExchangeRequest) { v.Binding.TaskID = "other" }, func(v *wire.ExchangeRequest) { v.Binding.SessionID = "other" }, func(v *wire.ExchangeRequest) { v.Binding.AdmissionID = "other" }, func(v *wire.ExchangeRequest) { v.Binding.IntentDigest = strings.Repeat("c", 64) }, func(v *wire.ExchangeRequest) { v.Binding.TaskClaimsDigest = "other" }, func(v *wire.ExchangeRequest) { v.Binding.Profile = "other" }, func(v *wire.ExchangeRequest) { v.Audience = "other" }, func(v *wire.ExchangeRequest) { v.Binding.Consumer = "other" }}
		for i, change := range changes {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				v := exchange
				change(&v)
				_, err := worker.Exchange(ctx, v)
				failure(t, err, "PermissionDenied")
			})
		}
		v := exchange
		v.Reference = uuid.NewString()
		_, err = worker.Exchange(ctx, v)
		failure(t, err, "NotFound")
	})
	t.Run("same_lineage_different_parent_and_conflicting_intent", func(t *testing.T) {
		newParent, newClaims, err := authority.signer.StartTask(codefly.StartTaskInput{Audience: parent.Audience, TenantID: parent.TenantId, OwnerPrincipalID: parent.OwnerPrincipalId, TaskID: parent.TaskId, SessionID: parent.SessionId, AuthorizationRevision: parent.AuthorizationRevision, ReplayPolicy: parent.ReplayPolicy, AuthorityScopes: parent.AuthorityScopes, ActorChain: parent.ActorChain, AttributionTeamIDs: parent.AttributionTeamIds, TTL: 10 * time.Minute})
		require.NoError(t, err)
		require.True(t, protoEqualCustodyLineage(parent, newClaims))
		conflict := input
		conflict.ParentToken = newParent.Encoded()
		_, err = caller.Register(ctx, pair.AccessToken, conflict)
		failure(t, err, "AlreadyExists")
		conflict = input
		conflict.Binding.IntentDigest = strings.Repeat("c", 64)
		_, err = caller.Register(ctx, pair.AccessToken, conflict)
		failure(t, err, "AlreadyExists")
	})
	t.Run("restart_request_loss_replacement_worker_bounded_children", func(t *testing.T) {
		ts.Close()
		store.Close()
		store = openStore()
		svc, err = business.NewService(store)
		require.NoError(t, err)
		WithService(svc)
		authority.Configure(WorkContextAuthorityConfiguration{Issuer: "example.work", KeyID: "example-key", PrivateKey: key, Authority: store})
		config.Store = store
		ts, worker, other, caller = serve()
		child, err := worker.Exchange(ctx, exchange)
		require.NoError(t, err)
		tok, err := codefly.ParseWorkContextToken(child.Token)
		require.NoError(t, err)
		claims, err := authority.verifier.Verify(tok, codefly.WorkContextExpectations{Issuer: authority.issuer, Audience: policy.Audience})
		require.NoError(t, err)
		require.LessOrEqual(t, claims.ExpiresAtUnix, registration.ExpiresAt)
		require.Equal(t, parent.AuthorizationRevision, claims.AuthorizationRevision)
		require.Equal(t, parent.TaskId, claims.TaskId)
		require.True(t, protoEqualCustodyLineage(parent, claims))
		lookup := exchange
		lookup.Lookup = true
		read, err := worker.Exchange(ctx, lookup)
		require.NoError(t, err)
		tok, err = codefly.ParseWorkContextToken(read.Token)
		require.NoError(t, err)
		claims, err = authority.verifier.Verify(tok, codefly.WorkContextExpectations{Issuer: authority.issuer, Audience: policy.Audience})
		require.NoError(t, err)
		require.Error(t, codefly.RequireWorkContextScope(claims, codefly.WorkContextScopeRequirement{ResourceKind: policy.ResourceKind, ResourceID: policy.ResourceID, Action: policy.InvokeAction, RequireExplicitResource: true}))
		require.NoError(t, codefly.RequireWorkContextScope(claims, codefly.WorkContextScopeRequirement{ResourceKind: policy.ResourceKind, ResourceID: policy.ResourceID, Action: policy.ReadAction, RequireExplicitResource: true}))
		taskTok, err := codefly.ParseWorkContextToken(registration.TaskToken)
		require.NoError(t, err)
		task, err := authority.verifier.Verify(taskTok, codefly.WorkContextExpectations{Issuer: authority.issuer, Audience: policy.TaskAudience})
		require.NoError(t, err)
		evidence, err := protojson.Marshal(task)
		require.NoError(t, err)
		var decoded base.WorkContextV1
		require.NoError(t, protojson.Unmarshal(evidence, &decoded))
		digest, err := wire.ClaimsDigest(&decoded)
		require.NoError(t, err)
		require.Equal(t, registration.Binding.TaskClaimsDigest, digest)
	})
	t.Run("recover_without_original_request_or_parent", func(t *testing.T) {
		in := wire.RecoverRequest{Binding: input.Binding, TaskExpiresAt: input.TaskExpiresAt}
		recovered, err := caller.Recover(ctx, pair.AccessToken, in)
		require.NoError(t, err)
		require.True(t, recovered == registration, "recovery changed original registration")
		wrong := in
		wrong.Binding.IntentDigest = strings.Repeat("c", 64)
		_, err = caller.Recover(ctx, pair.AccessToken, wrong)
		failure(t, err, "PermissionDenied")
		absent := in
		absent.Binding.AdmissionID = "absent"
		_, err = caller.Recover(ctx, pair.AccessToken, absent)
		failure(t, err, "NotFound")
		_, err = caller.Recover(ctx, "revision-only", in)
		failure(t, err, "Unauthenticated")
	})
	t.Run("scope_injection_rejected", func(t *testing.T) {
		body, _ := json.Marshal(exchange)
		body = append(body[:len(body)-1], []byte(`,"scopes":["*"]}`)...)
		req, err := http.NewRequest(http.MethodPost, ts.URL+wire.ExchangePath, strings.NewReader(string(body)))
		require.NoError(t, err)
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: workerTLS}}
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 400, resp.StatusCode)
	})
	t.Run("vault_key_rotation_preserves_original_binding", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, os.Getenv("CUSTODY_TEST_VAULT")+"/v1/transit/keys/api-keys/rotate", strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set("X-Vault-Token", os.Getenv("CUSTODY_TEST_VAULT_ROOT"))
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		_, err = worker.Exchange(ctx, exchange)
		require.NoError(t, err)
	})
	t.Run("signing_key_rotation_denies_without_rebinding", func(t *testing.T) {
		_, rotated, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		authority.Configure(WorkContextAuthorityConfiguration{Issuer: "example.work", KeyID: "rotated-key", PrivateKey: rotated, Authority: store})
		_, err = worker.Exchange(ctx, exchange)
		failure(t, err, "PermissionDenied")
		authority.Configure(WorkContextAuthorityConfiguration{Issuer: "example.work", KeyID: "example-key", PrivateKey: key, Authority: store})
		_, err = worker.Exchange(ctx, exchange)
		require.NoError(t, err)
	})
	t.Run("stored_horizon_substitution", func(t *testing.T) {
		sql(`UPDATE execution_custody SET expires_at=expires_at+interval '20 seconds' WHERE reference=$1`, registration.Reference)
		defer sql(`UPDATE execution_custody SET expires_at=to_timestamp($2) WHERE reference=$1`, registration.Reference, registration.ExpiresAt)
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "PermissionDenied")
	})
	t.Run("custody_outage", func(t *testing.T) {
		sql(`ALTER TABLE execution_custody RENAME TO execution_custody_offline`)
		defer sql(`ALTER TABLE execution_custody_offline RENAME TO execution_custody`)
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "Unavailable")
	})
	t.Run("accounts_outage", func(t *testing.T) {
		sql(`ALTER TABLE organization_authorization_revisions RENAME TO organization_authorization_revisions_offline`)
		defer sql(`ALTER TABLE organization_authorization_revisions_offline RENAME TO organization_authorization_revisions`)
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "Unavailable")
	})
	t.Run("vault_access_outage_fails_closed", func(t *testing.T) {
		update := func(policy string) {
			data, _ := json.Marshal(map[string]string{"policy": policy})
			req, err := http.NewRequest(http.MethodPut, os.Getenv("CUSTODY_TEST_VAULT")+"/v1/sys/policies/acl/custody", strings.NewReader(string(data)))
			require.NoError(t, err)
			req.Header.Set("X-Vault-Token", os.Getenv("CUSTODY_TEST_VAULT_ROOT"))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, 204, resp.StatusCode)
		}
		update(`path "transit/*" { capabilities = ["deny"] }`)
		defer update(`path "transit/encrypt/api-keys" { capabilities = ["update"] }
path "transit/decrypt/api-keys" { capabilities = ["update"] }`)
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "Unavailable")
	})
	t.Run("real_accounts_subprocess_restart_and_owner_recovery", func(t *testing.T) {
		binary := os.Getenv("CUSTODY_TEST_BINARY")
		require.NotEmpty(t, binary)
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0700))
		write := func(name string, data []byte) string {
			path := filepath.Join(dir, name)
			require.NoError(t, os.WriteFile(path, data, 0600))
			return path
		}
		signingDER, err := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, err)
		tlsDER, err := x509.MarshalPKCS8PrivateKey(serverTLS.Certificates[0].PrivateKey)
		require.NoError(t, err)
		cfg := map[string]any{
			"reader_url": os.Getenv("CUSTODY_TEST_READER"), "writer_url": os.Getenv("CUSTODY_TEST_WRITER"), "vault_url": os.Getenv("CUSTODY_TEST_VAULT"), "vault_token": os.Getenv("CUSTODY_TEST_VAULT_TOKEN"),
			"signing_key_file": write("signing.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signingDER})),
			"tls_cert_file":    write("server.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverTLS.Certificates[0].Certificate[0]})),
			"tls_key_file":     write("server-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: tlsDER})),
			"client_ca_file":   write("ca.pem", caPEM), "internal_credential_file": write("revision", []byte(strings.Repeat("r", 48))),
			"owner_token_file": filepath.Join(dir, "owner-token"), "state_file": filepath.Join(dir, "state.json"), "owner_id": owner, "org_id": org, "issuer": "example.work", "auth_issuer": "example.accounts", "auth_audience": "example.accounts", "consumers": map[string]ExecutionConsumerPolicy{"example": policy},
		}
		data, err := json.Marshal(cfg)
		require.NoError(t, err)
		configPath := write("config.json", data)
		launch := func() (*exec.Cmd, map[string]string) {
			_ = os.Remove(filepath.Join(dir, "state.json"))
			process := exec.Command(binary, "--local-qualification", "--config", configPath, "--max-runtime", "2m")
			// Do not project test administrator credentials into the broker process.
			process.Env = []string{}
			require.NoError(t, process.Start())
			var state map[string]string
			until := time.Now().Add(10 * time.Second)
			for time.Now().Before(until) {
				raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
				if err == nil && json.Unmarshal(raw, &state) == nil {
					return process, state
				}
				time.Sleep(20 * time.Millisecond)
			}
			_ = process.Process.Kill()
			_ = process.Wait()
			t.Fatal("Accounts process readiness unavailable")
			return nil, nil
		}
		process, state := launch()
		defer func() {
			if process != nil {
				_ = process.Process.Signal(os.Interrupt)
				_ = process.Wait()
			}
		}()
		ownerToken, err := os.ReadFile(filepath.Join(dir, "owner-token"))
		require.NoError(t, err)
		connection, err := grpc.NewClient(state["tenant_grpc"], grpc.WithTransportCredentials(credentials.NewTLS(workerTLS)))
		require.NoError(t, err)
		tenant := gen.NewWorkContextServiceClient(connection)
		call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+string(ownerToken))), 3*time.Second)
		original, err := tenant.StartTask(call, start)
		cancel()
		require.NoError(t, err)
		require.NoError(t, connection.Close())
		verify, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{PublicKeys: map[string]ed25519.PublicKey{jwt.KeyID(): key.Public().(ed25519.PublicKey)}})
		require.NoError(t, err)
		tok, err := codefly.ParseWorkContextToken(original.Token)
		require.NoError(t, err)
		claims, err := verify.Verify(tok, codefly.WorkContextExpectations{Issuer: "example.work"})
		require.NoError(t, err)
		client, err := wire.NewClient(state["broker_url"], &http.Transport{TLSClientConfig: workerTLS})
		require.NoError(t, err)
		input := wire.RegisterRequest{Binding: input.Binding, ParentToken: original.Token, TaskExpiresAt: claims.ExpiresAtUnix - 20}
		input.Binding.AdmissionID = "process-" + uuid.NewString()
		registered, err := client.Register(ctx, string(ownerToken), input)
		require.NoError(t, err)
		immutable := wire.RecoverRequest{Binding: input.Binding, TaskExpiresAt: input.TaskExpiresAt}
		// The caller deliberately discards all original parent bytes before restart.
		input.ParentToken = ""
		original = nil
		require.NoError(t, process.Process.Signal(os.Interrupt))
		require.NoError(t, process.Wait())
		process = nil
		process, state = launch()
		replacement, err := wire.NewClient(state["broker_url"], &http.Transport{TLSClientConfig: workerTLS})
		require.NoError(t, err)
		ownerToken, err = os.ReadFile(filepath.Join(dir, "owner-token"))
		require.NoError(t, err)
		recovered, err := replacement.Recover(ctx, string(ownerToken), immutable)
		require.NoError(t, err)
		require.True(t, recovered == registered, "subprocess recovery replaced original child")
		child, err := replacement.Exchange(ctx, wire.ExchangeRequest{Reference: recovered.Reference, Binding: recovered.Binding, Audience: policy.Audience, Lookup: true})
		require.NoError(t, err)
		tok, err = codefly.ParseWorkContextToken(child.Token)
		require.NoError(t, err)
		bounded, err := verify.Verify(tok, codefly.WorkContextExpectations{Issuer: "example.work", Audience: policy.Audience})
		require.NoError(t, err)
		require.LessOrEqual(t, bounded.ExpiresAtUnix, recovered.ExpiresAt)
		revisionTLS := workerTLS.Clone()
		revisionTLS.Certificates = nil // revision uses server TLS + internal token, no worker identity
		revisionConn, err := grpc.NewClient(state["internal_grpc"], grpc.WithTransportCredentials(credentials.NewTLS(revisionTLS)))
		require.NoError(t, err)
		defer revisionConn.Close()
		revisionClient := gen.NewWorkContextServiceClient(revisionConn)
		revisionCall := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-codefly-internal-token", strings.Repeat("r", 48)))
		_, err = revisionClient.CheckAuthorizationRevision(revisionCall, &gen.CheckAuthorizationRevisionRequest{OrgId: org, OwnerPrincipalId: owner, AuthorizationRevision: claims.AuthorizationRevision, Subjects: []*gen.WorkContextRevisionSubject{{PrincipalId: owner, Scopes: start.AuthorityScopes}, {PrincipalId: actor, Scopes: start.AuthorityScopes}}})
		require.NoError(t, err)
		httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: workerTLS}, Timeout: 3 * time.Second}
		response, err := httpClient.Get(state["jwks_url"])
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, 200, response.StatusCode)
		var jwks struct {
			Keys []json.RawMessage `json:"keys"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&jwks))
		require.Len(t, jwks.Keys, 1)
		_, err = gen.NewWorkContextServiceClient(revisionConn).StartTask(metadata.NewOutgoingContext(ctx, metadata.Pairs("x-codefly-internal-token", strings.Repeat("r", 48))), start)
		require.Error(t, err)
	})
	t.Run("stale_authorization_revision", func(t *testing.T) {
		sql(`UPDATE organization_authorization_revisions SET revision=$2 WHERE org_id=$1`, org, int64(parent.AuthorizationRevision)+1)
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "FailedPrecondition")
	})
	t.Run("actor_chain_revocation", func(t *testing.T) {
		require.NoError(t, store.RevokeActorChainHop(ownerCtx, org, parent.ActorChain[0].DelegationId, owner, "Example qualification"))
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "PermissionDenied")
	})
	t.Run("expiry_cleanup_tombstone", func(t *testing.T) {
		sql(`UPDATE execution_custody SET expires_at=NOW()-interval '1 second' WHERE reference=$1`, registration.Reference)
		require.NoError(t, store.PurgeExecutionCustody(ctx, time.Now()))
		var envelope string
		require.NoError(t, admin.QueryRow(ctx, `SELECT envelope FROM execution_custody WHERE reference=$1`, registration.Reference).Scan(&envelope))
		require.Empty(t, envelope)
		_, err := worker.Exchange(ctx, exchange)
		failure(t, err, "FailedPrecondition")
		_, err = caller.Recover(ctx, pair.AccessToken, wire.RecoverRequest{Binding: input.Binding, TaskExpiresAt: input.TaskExpiresAt})
		failure(t, err, "FailedPrecondition")
	})
}

func protoEqualCustodyLineage(a, b *base.WorkContextV1) bool {
	return custodyJSONHash(custodyLineage(a)) == custodyJSONHash(custodyLineage(b))
}

// Compile-time reminder: the integration calls actual auth, Vault and PostgreSQL.
var _ context.Context
