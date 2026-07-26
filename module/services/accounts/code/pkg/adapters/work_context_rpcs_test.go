package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type workContextAuthorityFake struct {
	facts *business.WorkContextAuthorityFacts
	err   error
}

func (f *workContextAuthorityFake) ResolveWorkContextAuthority(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []business.WorkContextPermission,
) (*business.WorkContextAuthorityFacts, error) {
	return f.facts, f.err
}

func TestWorkContextScopesMapExactPermissionChecks(t *testing.T) {
	permissions, scopes, err := workContextScopes([]*gen.WorkContextScope{{
		ResourceKind: "repository",
		Actions:      []string{"read", "write"},
		ResourceIds:  []string{"repo:one", "repo:two"},
	}})
	require.NoError(t, err)
	require.Equal(t, []*basev0.WorkScopeV1{{
		ResourceKind: "repository",
		Actions:      []string{"read", "write"},
		ResourceIds:  []string{"repo:one", "repo:two"},
	}}, scopes)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "repository", Action: "read", ResourceID: "repo:one"},
		{ResourceKind: "repository", Action: "read", ResourceID: "repo:two"},
		{ResourceKind: "repository", Action: "write", ResourceID: "repo:one"},
		{ResourceKind: "repository", Action: "write", ResourceID: "repo:two"},
	}, permissions)
}

func TestWorkContextParentVerificationRejectsStaleAuthorization(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authority := &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
		OrganizationRevision: 12,
		PrincipalRevision:    13,
	}}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  authority,
	})
	require.NoError(t, server.configureErr)

	token, _, err := server.signer.StartTask(codefly.StartTaskInput{
		Audience:              "consumer.test",
		TenantID:              "019f6bf7-5b4b-74e5-8c17-092259bb1661",
		OwnerPrincipalID:      "019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		TaskID:                "019f6bf7-1111-7111-8111-111111111111",
		SessionID:             "019f6bf7-2222-7222-8222-222222222222",
		AuthorizationRevision: 12,
		ReplayPolicy:          codefly.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{{
			ResourceKind: "evidence",
			Actions:      []string{"append"},
		}},
	})
	require.NoError(t, err)

	_, _, err = server.verifyParent(
		context.Background(),
		"019f6bf7-5b4b-74e5-8c17-092259bb1661",
		"019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		token.Encoded(),
	)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestWorkContextAuthorityConfigurationFailsClosed(t *testing.T) {
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{})
	require.Error(t, server.configureErr)
	require.Nil(t, server.signer)
	require.Nil(t, server.verifier)
	require.Nil(t, server.authority)
}
