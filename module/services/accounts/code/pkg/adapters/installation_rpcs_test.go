package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	installOrgID   = "019fec91-2000-7000-8000-0000000000a1"
	installOwnerID = "019fec91-2000-7000-8000-0000000000a2"
	installAgentID = "019fec91-2000-7000-8000-0000000000a3"
	installID      = "019fec91-2000-7000-8000-0000000000a4"
	installTaskID  = "019fec91-2000-7000-8000-0000000000a5"
	installSession = "019fec91-2000-7000-8000-0000000000a6"
)

func newInstallationMintServer(t *testing.T, facts *business.InstallationAuthorityFacts, err error) *WorkContextAuthorityServer {
	t.Helper()
	_, privateKey, genErr := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, genErr)
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  &workContextAuthorityFake{installationFcts: facts, installationErr: err},
	})
	require.NoError(t, server.configureErr)
	return server
}

func healthyInstallationFacts() *business.InstallationAuthorityFacts {
	return &business.InstallationAuthorityFacts{
		OwnerPrincipalID:     installOwnerID,
		OrganizationRevision: 7,
		Actor: &business.Principal{
			ID:               installAgentID,
			Kind:             business.PrincipalKindAgent,
			AgentIdentifier:  "acme.example/solution:1.0.0",
			AllowedAudiences: []string{"acme.collection"},
			AllowedScopes:    []string{"doc"},
		},
	}
}

func installInternalContext() context.Context {
	SetInternalToken("installation-mint-test-token")
	return metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-codefly-internal-token", "installation-mint-test-token"),
	)
}

func installTaskRequest() *gen.StartInstallationTaskRequest {
	return &gen.StartInstallationTaskRequest{
		OrgId:          installOrgID,
		InstallationId: installID,
		TaskId:         installTaskID,
		SessionId:      installSession,
		Audience:       "acme.collection",
		AuthorityScopes: []*gen.WorkContextScope{{
			ResourceKind: "doc",
			Actions:      []string{"write"},
			ResourceIds:  []string{"boundary-1"},
		}},
	}
}

func TestStartInstallationTaskMintsUnderOwnerOfRecordWithAgentActor(t *testing.T) {
	server := newInstallationMintServer(t, healthyInstallationFacts(), nil)
	t.Cleanup(func() { SetInternalToken("") })

	issued, err := server.StartInstallationTask(installInternalContext(), installTaskRequest())
	require.NoError(t, err)
	require.Equal(t, installOwnerID, issued.GetOwnerPrincipalId(), "owner is the owner of record")
	require.Equal(t, installAgentID, issued.GetCurrentActorPrincipalId(), "the agent is the current actor")

	// The signed capability itself carries the same owner and a single actor hop
	// naming the agent — the attributable, provable-on-behalf-of write.
	token, err := codefly.ParseWorkContextToken(issued.GetToken())
	require.NoError(t, err)
	claims, err := server.verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:           "accounts.test",
		TenantID:         installOrgID,
		OwnerPrincipalID: installOwnerID,
	})
	require.NoError(t, err)
	require.Len(t, claims.GetActorChain(), 1)
	require.Equal(t, installAgentID, claims.GetActorChain()[0].GetPrincipalId())
	require.Equal(t, business.PrincipalKindAgent, claims.GetActorChain()[0].GetPrincipalKind())
}

func TestStartInstallationTaskRequiresInternalCredential(t *testing.T) {
	server := newInstallationMintServer(t, healthyInstallationFacts(), nil)
	_, err := server.StartInstallationTask(context.Background(), installTaskRequest())
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestStartInstallationTaskEnforcesAgentCeiling(t *testing.T) {
	server := newInstallationMintServer(t, healthyInstallationFacts(), nil)
	t.Cleanup(func() { SetInternalToken("") })

	req := installTaskRequest()
	req.Audience = "some.other.audience"
	_, err := server.StartInstallationTask(installInternalContext(), req)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "audience outside the agent ceiling is refused at mint")
}

func TestStartInstallationTaskFailsClosedWhenAuthorityUnresolvable(t *testing.T) {
	server := newInstallationMintServer(t, nil,
		business.NewStoreError(business.ErrWorkContextAuthorizationStale, business.ErrTypeNotFound))
	t.Cleanup(func() { SetInternalToken("") })

	_, err := server.StartInstallationTask(installInternalContext(), installTaskRequest())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
