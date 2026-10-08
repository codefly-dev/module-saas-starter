package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
)

func TestVerifyWorkContextRuntimeBoundarySignedRead(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "test-key", PrivateKey: key, Authority: sourceReadIdentityAuthority{facts}})
	record := &business.SolutionRegistration{RuntimeBoundary: boundaryStoredSeed,
		Frontend: &business.SolutionFrontendHalf{LeaseExpiresAt: time.Now().Add(time.Hour), ContractVersion: "v1"},
		Backend:  &business.SolutionBackendHalf{LeaseExpiresAt: time.Now().Add(time.Hour), ContractVersion: "v1"}}
	store := &boundaryRegistryStore{record: record}
	service, err = business.NewService(store)
	require.NoError(t, err)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Tenant: readOrg}})
	boundary, err := business.SolutionRuntimeBoundary(boundaryStoredSeed, readOrg)
	require.NoError(t, err)
	moduleToken, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("rows"), Tenant: readOrg})
	require.NoError(t, err)
	mint := func(task, tenant, audience string) string {
		token, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{Audience: audience, TenantID: tenant, OwnerPrincipalID: readOwner, TaskID: task, SessionID: "019f6bf7-2222-7222-8222-222222222222", AuthorizationRevision: facts.facts.EffectiveRevision(), ReplayPolicy: workcontext.WorkContextReplayIdempotent})
		require.NoError(t, err)
		return token.Encoded()
	}
	token := mint(boundary, readOrg, "rows")
	request := func(token string) *connect.Request[gen.VerifyWorkContextRuntimeBoundaryRequest] {
		req := connect.NewRequest(&gen.VerifyWorkContextRuntimeBoundaryRequest{ForwardedWorkContextToken: token})
		req.Header().Set(workcontext.WorkContextHeaderName, moduleToken.Encoded())
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		return req
	}
	ctx := context.Background()
	result, err := client.VerifyWorkContextRuntimeBoundary(ctx, request(token))
	require.NoError(t, err)
	require.Equal(t, readOrg, result.Msg.TenantId)
	require.Equal(t, boundary, result.Msg.BoundaryId)
	require.NotEqual(t, boundaryStoredSeed, result.Msg.BoundaryId)
	require.Nil(t, store.saved, "attestation must not write registration")
	for _, tc := range []struct{ name, token string }{
		{"ordinary task", mint("019f6bf7-1111-7111-8111-111111111111", readOrg, "rows")},
		{"seed itself", mint(boundaryStoredSeed, readOrg, "rows")},
		{"wrong audience", mint(boundary, readOrg, "documents")},
		{"wrong tenant", mint(boundary, "019f6bf7-4444-7444-8444-444444444444", "rows")},
		{"invalid signature", token[:len(token)-8] + "invalid!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.VerifyWorkContextRuntimeBoundary(ctx, request(tc.token))
			require.Error(t, err)
		})
	}
	for _, header := range []string{workcontext.WorkContextHeaderName, "x-codefly-internal-token"} {
		req := request(token)
		req.Header().Del(header)
		_, err := client.VerifyWorkContextRuntimeBoundary(ctx, req)
		require.Error(t, err)
	}
	// A valid signature does not rescue an expired forwarded capability.
	oldSigner, err := workcontext.NewWorkContextSigner(workcontext.WorkContextSignerOptions{Issuer: "accounts.test", KeyID: "test-key", PrivateKey: key, Now: func() time.Time { return time.Now().Add(-time.Hour) }})
	require.NoError(t, err)
	expired, _, err := oldSigner.StartTask(workcontext.StartTaskInput{Audience: "rows", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: boundary, SessionID: "old-session", AuthorizationRevision: facts.facts.EffectiveRevision()})
	require.NoError(t, err)
	_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(expired.Encoded()))
	require.Error(t, err)
	// Every delegated hop remains subject to live revocation, with no new actors.
	workContextSingleton.authority = &readExchangeAuthority{facts: facts.facts}
	journal := &readExchangeJournal{}
	workContextSingleton.journal = journal
	actors := []*basev0.WorkActorV1{{PrincipalId: "actor-1", PrincipalKind: "service", DelegationId: "hop-1"}, {PrincipalId: "actor-2", PrincipalKind: "service", DelegationId: "hop-2"}}
	delegated, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{Audience: "rows", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: boundary, SessionID: "delegated-session", AuthorizationRevision: facts.facts.EffectiveRevision(), ActorChain: actors})
	require.NoError(t, err)
	_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(delegated.Encoded()))
	require.NoError(t, err)
	require.Equal(t, []string{"hop-1", "hop-2"}, journal.ids)
	for _, hop := range []string{"hop-1", "hop-2"} {
		journal.revoked = hop
		_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(delegated.Encoded()))
		require.Error(t, err)
	}
	workContextSingleton.journal = nil
	_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(delegated.Encoded()))
	require.Error(t, err)
	workContextSingleton.authority = sourceReadIdentityAuthority{facts}
	duplicate := request(token)
	duplicate.Header().Add(workcontext.WorkContextHeaderName, moduleToken.Encoded())
	_, err = client.VerifyWorkContextRuntimeBoundary(ctx, duplicate)
	require.Error(t, err)
	// Cross-tenant access needs an explicit current module grant and the other
	// tenant's derived boundary; neither tenant nor boundary comes from the body.
	otherTenant := "019f6bf7-4444-7444-8444-444444444444"
	otherBoundary, err := business.SolutionRuntimeBoundary(boundaryStoredSeed, otherTenant)
	require.NoError(t, err)
	otherToken := mint(otherBoundary, otherTenant, "rows")
	_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(otherToken))
	require.Error(t, err)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Tenant: readOrg, CrossTenant: true}})
	other, err := client.VerifyWorkContextRuntimeBoundary(ctx, request(otherToken))
	require.NoError(t, err)
	require.Equal(t, otherTenant, other.Msg.TenantId)
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Tenant: readOrg}})
	facts.facts.OrganizationRevision++
	_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(token))
	require.Error(t, err)
	facts.facts.OrganizationRevision--
	now := time.Now()
	for _, mutate := range []func(){
		func() { record.TombstonedAt = &now },
		func() { record.TombstonedAt = nil; record.Backend.LeaseExpiresAt = now.Add(-time.Minute) },
		func() { record.Backend.LeaseExpiresAt = now.Add(time.Hour); record.Frontend.ContractVersion = "v2" },
		func() { record.Frontend.ContractVersion = "v1"; record.Frontend.LeaseExpiresAt = now.Add(-time.Minute) },
		func() { record.Frontend = nil },
	} {
		mutate()
		_, err = client.VerifyWorkContextRuntimeBoundary(ctx, request(token))
		require.Error(t, err)
	}
	require.Nil(t, store.saved)
}
