package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The accounts half of the MCP module read (the gateway half is
// TestMCPModuleReadThroughTheRealGatewayOnCoreWorkContexts, in auth-gateway; the
// two together, the real gateway in front of this StartTask, are
// TestTheModuleReadThroughTheRealGatewayIsMintedByTheRealStartTask in
// qualification/module-authority).
//
// A consumer's runtime mints a capability for the composed module it declared it
// consumes, forwarding the viewer's credential. What the real StartTask handler
// signs for that request is exactly what the gateway's module route and the
// module itself hold it to: core's encoding, the viewer as owner, their
// organization as tenant, the module as audience, sealed to the organization's
// installation of the module with no execution (a person bears none), and in the
// session the viewer's credential authenticated — never one the request named.
//
// It is read here by a verifier that shares nothing with the issuer but the
// public key and the issuer's live answers: core's Verifier, built field by field
// the way a consuming module builds one.
func TestStartTaskMintsWhatACoreVerifierReadsForAModule(t *testing.T) {
	const confinedSession = "019f6bf7-2222-7222-8222-222222222299"
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	seals := newSealAnswersFake(12)
	server := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, seals)
	ctx := accountsauth.WithVerifiedSessionIDString(
		stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{}), confinedSession)

	issued, err := server.StartTask(ctx, &gen.StartTaskWorkContextRequest{
		OrgId:           renewOrgID,
		TaskId:          renewTaskID,
		SessionId:       "019f6bf7-0000-7000-8000-000000000000", // named by the runtime; not what is signed
		Audience:        "documents",
		TtlSeconds:      300,
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}},
	})
	require.NoError(t, err)

	consumer := &workcontext.Verifier{
		Issuer:    server.issuer,
		Audience:  "documents",
		Keys:      server.coreVerifier(renewOrgID, renewActorID, "documents").Keys,
		Revisions: ownerRevisions{answers: seals, owner: renewActorID},
		Replay:    workcontext.NewMemoryReplayStore(),
		Grants:    noApprovalGrants{},
		Seals:     hostSealSource{answers: seals, tenant: renewOrgID},
	}
	verified, err := consumer.Verify(context.Background(), issued.GetToken())
	require.NoError(t, err)
	claims := verified.Context()
	require.Equal(t, renewActorID, claims.GetOwnerPrincipalId())
	require.Equal(t, business.PrincipalKindHuman, claims.GetOwnerPrincipalKind())
	require.Equal(t, renewOrgID, claims.GetTenantId())
	require.Equal(t, confinedSession, claims.GetSessionId(), "the session the credential authenticated, which the gateway pairs on")
	require.Equal(t, testInstallation("documents"), claims.GetSeal().GetInstallationId())
	require.Empty(t, claims.GetSeal().GetImageDigest(), "a person bears no execution")
	require.Empty(t, claims.GetActorChain())
	require.Equal(t, []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}}, claims.GetAuthorityScopes())

	// A module this organization never installed has nothing to seal the
	// capability to, and is minted nothing.
	seals.uninstalled["documents"] = true
	_, err = server.StartTask(ctx, &gen.StartTaskWorkContextRequest{
		OrgId: renewOrgID, TaskId: renewTaskID, SessionId: confinedSession, Audience: "documents", TtlSeconds: 300,
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not installed in this organization")
}

// StartChildSession and StartInstallationTask assign the session they sign,
// whatever the request carried: a session a caller could name is one it could
// name after another credential's, and the gateway pairs a confined credential
// with a capability on the session alone.
func TestDerivedSessionsAreAssignedByTheIssuer(t *testing.T) {
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	server := newRenewTestServer(t)
	parent := mintOwnedContext(t, server, renewSession)
	ctx := accountsauth.WithVerifiedSessionIDString(
		stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{}), renewSession)

	first, err := server.StartChildSession(ctx, &gen.StartChildSessionWorkContextRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parent, ActorPrincipalId: renewActorID,
		Audience: "tool.test", TtlSeconds: 60,
		GrantedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	require.NoError(t, err)
	second, err := server.StartChildSession(ctx, &gen.StartChildSessionWorkContextRequest{
		OrgId: renewOrgID, ParentWorkContextToken: parent, ActorPrincipalId: renewActorID,
		Audience: "tool.test", TtlSeconds: 60,
		GrantedScopes: []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	require.NoError(t, err)

	for _, issued := range []*gen.IssuedWorkContext{first, second} {
		require.NotEmpty(t, issued.GetSessionId())
		require.NotEqual(t, renewSession, issued.GetSessionId())
		require.Equal(t, renewSession, issued.GetParentSessionId())
	}
	require.NotEqual(t, first.GetSessionId(), second.GetSessionId(), "each child session is fresh")
}
