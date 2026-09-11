package main

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
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/cache"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This calls the normal host's projection, construction, bind and shutdown
// functions with actual PostgreSQL sessions/authority and Redis revocation.
// Only network discovery is replaced with allocated loopback sockets.
func TestExecutionTenantReal(t *testing.T) {
	if os.Getenv("CUSTODY_TEST_ADMIN") == "" {
		t.Skip("run qualification/execution-custody/run.py --tenant-mount")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, os.Getenv("CUSTODY_TEST_ADMIN"))
	require.NoError(t, err)
	defer admin.Close()
	sql := func(q string, args ...any) { t.Helper(); _, e := admin.Exec(ctx, q, args...); require.NoError(t, e) }
	owner, org, actor, role := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	task, session := uuid.NewString(), uuid.NewString()
	sql(`INSERT INTO users(uuid,primary_email,status) VALUES($1,'user@example.com','active')`, owner)
	sql(`INSERT INTO organizations(id,name,slug,owner_id) VALUES($1,'Example','example',$2)`, org, owner)
	sql(`INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'owner')`, org, owner)
	sql(`INSERT INTO principals(id,kind,display_name) VALUES($1,'human','Example owner') ON CONFLICT DO NOTHING`, owner)
	sql(`INSERT INTO principals(id,kind,display_name,org_id,agent_identifier,allowed_audiences,allowed_scopes) VALUES($1,'agent','Example agent',$2,'example.test/agent:1',ARRAY['example.facade','example.tasks','example.model'],ARRAY['example.tasks','example.model'])`, actor, org)
	sql(`INSERT INTO roles(id,name,org_id) VALUES($1,'example-execution',$2)`, role, org)
	for _, action := range []string{"start", "execute", "read"} {
		sql(`INSERT INTO role_permissions(role_id,resource,action) VALUES($1,'example.tasks',$2)`, role, action)
	}
	sql(`INSERT INTO role_assignments(subject_id,subject_kind,role_id,org_id) VALUES($1,'principal',$2,$3)`, actor, role, org)
	// The disposable DB is loopback-only; its existing fixture DSNs are not a
	// managed transport proof. Transport parsing has separate real socket tests.
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "")
	store, err := infra.NewPostgresStoreWithCapabilities(ctx, os.Getenv("CUSTODY_TEST_READER"), os.Getenv("CUSTODY_TEST_WRITER"))
	require.NoError(t, err)
	defer store.Close()
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "verified-tls")
	svc, err := business.NewService(store)
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	jwt := minter.New(minter.Config{Issuer: "saas-starter", Audience: "saas-starter"}, key, pgauth.NewSessionStore(store))
	redis, closeRedis, err := cache.NewRedis(os.Getenv("CUSTODY_TEST_REDIS"))
	require.NoError(t, err)
	defer closeRedis()
	revoker := cache.NewTokenRevoker(redis)
	jwt.SetRevoker(revoker)
	svc.SetJWTMinter(jwt)
	adapters.WithService(svc)
	defer adapters.WithService(nil)
	internal := strings.Repeat("i", 48)
	t.Setenv("CODEFLY_INTERNAL_TOKEN", internal)
	adapters.SetInternalToken(internal)
	defer adapters.SetInternalToken("")
	identity := &auth.Identity{UserID: uuid.MustParse(owner), OrgID: uuid.MustParse(org), OrgRole: "owner", SessionID: uuid.New()}
	pair, err := jwt.Mint(ctx, identity)
	require.NoError(t, err)
	cipher := infra.NewVaultClientDirect(os.Getenv("CUSTODY_TEST_VAULT"), os.Getenv("CUSTODY_TEST_VAULT_TOKEN"))
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0400))
		return p
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Example local tenant"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(certPEM))
	trusted := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	config := executionCustodyProjection{TLSCertFile: write("cert", certPEM), TLSKeyFile: write("key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})), ClientCAFile: write("ca", certPEM), Consumers: map[string]adapters.ExecutionConsumerPolicy{"example": {WorkerURI: "spiffe://example.test/worker", ParentAudience: "example.facade", TaskAudience: "example.tasks", Audience: "example.model", Profile: "example-profile@1", ResourceKind: "example.model", ResourceID: "example-profile", InvokeAction: "invoke", ReadAction: "read", TaskResourceKind: "example.tasks", TaskActions: []string{"execute", "read", "start"}}}}
	configure := func(enabled bool) *executionCustodyHost {
		t.Helper()
		config.EnableTenant = enabled
		raw, e := json.Marshal(config)
		require.NoError(t, e)
		p := filepath.Join(dir, uuid.NewString())
		require.NoError(t, os.WriteFile(p, raw, 0400))
		t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", p)
		h, e := configuredExecutionCustody(store, cipher, jwt, jwt.KeyID(), key, true, false)
		require.NoError(t, e)
		return h
	}
	start := &gen.StartTaskWorkContextRequest{OrgId: org, TaskId: task, SessionId: session, Audience: "example.facade", ActorPrincipalId: actor, TtlSeconds: 120, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example.tasks", ResourceIds: []string{task}, Actions: []string{"execute", "read", "start"}}}}
	ownerContext := func() context.Context {
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+pair.AccessToken))
	}
	dial := func(addr string, transport credentials.TransportCredentials) *grpc.ClientConn {
		t.Helper()
		c, e := grpc.NewClient(addr, grpc.WithTransportCredentials(transport), grpc.WithDisableRetry())
		require.NoError(t, e)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	invoke := func(c *grpc.ClientConn, call context.Context) (*gen.IssuedWorkContext, error) {
		call, cancel := context.WithTimeout(call, time.Second)
		defer cancel()
		return gen.NewWorkContextServiceClient(c).StartTask(call, start)
	}
	bind := func(h *executionCustodyHost) (map[string]string, func()) {
		t.Helper()
		addresses := map[string]string{}
		stop, e := startExecutionCustodyWithListener(h, func(name string) (net.Listener, error) {
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e == nil {
				addresses[name] = l.Addr().String()
			}
			return l, e
		})
		require.NoError(t, e)
		t.Cleanup(stop)
		return addresses, stop
	}
	t.Run("explicit_opt_in", func(t *testing.T) {
		h := configure(false)
		require.Nil(t, h.tenant)
		addresses, stop := bind(h)
		require.Len(t, addresses, 2)
		stop()
	})
	t.Run("bind_failure_rolls_back_all_listeners", func(t *testing.T) {
		h := configure(true)
		opened := []string{}
		stop, e := startExecutionCustodyWithListener(h, func(name string) (net.Listener, error) {
			if name == "tenant" {
				return nil, errors.New("fixture occupied endpoint")
			}
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e == nil {
				opened = append(opened, l.Addr().String())
			}
			return l, e
		})
		require.Error(t, e)
		require.Nil(t, stop)
		require.Len(t, opened, 2)
		for _, addr := range opened {
			l, e := net.Listen("tcp", addr)
			require.NoError(t, e)
			require.NoError(t, l.Close())
		}
	})
	addresses, stop := bind(configure(true))
	require.Len(t, addresses, 3)
	tenantConn := dial(addresses["tenant"], credentials.NewTLS(trusted))
	tenant := gen.NewWorkContextServiceClient(tenantConn)
	var parent *gen.IssuedWorkContext
	t.Run("native_grpc_original_owner_start_and_exchange", func(t *testing.T) {
		parent, err = invoke(tenantConn, ownerContext())
		require.NoError(t, err)
		require.NotEmpty(t, parent.Token)
		_, e := tenant.ExchangeAudience(ownerContext(), &gen.ExchangeWorkContextAudienceRequest{OrgId: org, ParentWorkContextToken: parent.Token, Audience: "example.tasks", TtlSeconds: 60, AttenuatedScopes: start.AuthorityScopes, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT})
		require.NoError(t, e)
	})
	t.Run("missing_forged_and_revision_credentials_denied", func(t *testing.T) {
		for _, call := range []context.Context{ctx, metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer invalid")), metadata.NewOutgoingContext(ctx, metadata.Pairs("x-codefly-internal-token", internal, "x-user-id", owner, "x-org-id", org))} {
			_, e := invoke(tenantConn, call)
			require.Equal(t, codes.Unauthenticated, status.Code(e))
		}
	})
	t.Run("tls_trust_hostname_version_and_plaintext_denied", func(t *testing.T) {
		for name, tc := range map[string]*tls.Config{"untrusted": {RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS13}, "hostname": {RootCAs: roots, ServerName: "wrong.example", MinVersion: tls.VersionTLS13}, "obsolete_version": {RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}} {
			t.Run(name, func(t *testing.T) {
				_, e := invoke(dial(addresses["tenant"], credentials.NewTLS(tc)), ownerContext())
				require.Error(t, e)
			})
		}
		_, e := invoke(dial(addresses["tenant"], insecure.NewCredentials()), ownerContext())
		require.Error(t, e)
	})
	require.NotNil(t, parent)
	token, e := codefly.ParseWorkContextToken(parent.Token)
	require.NoError(t, e)
	verifier, e := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{PublicKeys: map[string]ed25519.PublicKey{jwt.KeyID(): key.Public().(ed25519.PublicKey)}})
	require.NoError(t, e)
	claims, e := verifier.Verify(token, codefly.WorkContextExpectations{Issuer: "saas-starter", Audience: "example.facade"})
	require.NoError(t, e)
	check := &gen.CheckAuthorizationRevisionRequest{OrgId: org, OwnerPrincipalId: owner, AuthorizationRevision: claims.AuthorizationRevision, Subjects: []*gen.WorkContextRevisionSubject{{PrincipalId: owner, Scopes: start.AuthorityScopes}, {PrincipalId: actor, Scopes: start.AuthorityScopes}}}
	internalCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-codefly-internal-token", internal))
	t.Run("tenant_and_revision_exposure_remain_separate", func(t *testing.T) {
		_, e := tenant.CheckAuthorizationRevision(internalCtx, check)
		require.Equal(t, codes.PermissionDenied, status.Code(e))
		rev := gen.NewWorkContextServiceClient(dial(addresses["revision"], credentials.NewTLS(trusted)))
		_, e = rev.CheckAuthorizationRevision(internalCtx, check)
		require.NoError(t, e)
		_, e = rev.StartTask(ownerContext(), start)
		require.Equal(t, codes.PermissionDenied, status.Code(e))
	})
	t.Run("shutdown_rebind_and_original_parent_survives", func(t *testing.T) {
		stop()
		stop()
		for _, addr := range addresses {
			l, e := net.Listen("tcp", addr)
			require.NoError(t, e)
			require.NoError(t, l.Close())
		}
		_, e := invoke(tenantConn, ownerContext())
		require.Error(t, e)
		next, nextStop := bind(configure(true))
		defer nextStop()
		client := gen.NewWorkContextServiceClient(dial(next["tenant"], credentials.NewTLS(trusted)))
		child, e := client.ExchangeAudience(ownerContext(), &gen.ExchangeWorkContextAudienceRequest{OrgId: org, ParentWorkContextToken: parent.Token, Audience: "example.tasks", TtlSeconds: 60, AttenuatedScopes: start.AuthorityScopes, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT})
		require.NoError(t, e)
		tok, e := codefly.ParseWorkContextToken(child.Token)
		require.NoError(t, e)
		bounded, e := verifier.Verify(tok, codefly.WorkContextExpectations{Issuer: "saas-starter", Audience: "example.tasks"})
		require.NoError(t, e)
		require.True(t, bounded.TaskId == claims.TaskId && bounded.SessionId == claims.SessionId && bounded.ExpiresAtUnix <= claims.ExpiresAtUnix)
		require.NoError(t, revoker.RevokeSession(ctx, identity.SessionID.String(), time.Minute))
		_, e = client.StartTask(ownerContext(), start)
		require.Equal(t, codes.Unauthenticated, status.Code(e))
	})
}
