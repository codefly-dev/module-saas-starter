package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func TestResourceKind(t *testing.T) {
	require.Equal(t, "repo", resourceKind("repo:codefly-dev/codefly.dev"))
	require.Equal(t, "file", resourceKind("file:/etc/passwd"))
	require.Equal(t, "repo", resourceKind("repo"), "a bare kind with no instance is itself the kind")
	require.Equal(t, "", resourceKind(""), "an empty resource carries no kind")
}

// TestMintApprovedTokenEnforcesResourceCeiling proves the registered agent
// ceiling is a hard cap over the delegation mint path — not only the Work
// Context path — so a grantor's approval cannot elevate an agent onto a resource
// kind outside its allowed_scopes.
func TestMintApprovedTokenEnforcesResourceCeiling(t *testing.T) {
	// 32-byte HMAC secret: the minimum policy.Mint accepts.
	server := &DelegationServer{SigningSecret: []byte("0123456789abcdef0123456789abcdef")}

	capped := &business.Principal{
		ID:              "019f6bf7-1111-7111-8111-111111111111",
		Kind:            business.PrincipalKindAgent,
		OrgID:           "019f6bf7-2222-7222-8222-222222222222",
		AgentIdentifier: "publisher/capped:1.0.0",
		AllowedScopes:   []string{"repo"},
	}

	t.Run("resource kind outside the ceiling mints no token", func(t *testing.T) {
		_, sa, err := server.mintApprovedToken(capped, &business.DelegationGrant{
			Action:   "read",
			Resource: "secret:prod/api-key",
		})
		require.Error(t, err)
		require.Nil(t, sa)
		require.Contains(t, err.Error(), "outside agent")
	})

	t.Run("resource kind within the ceiling mints", func(t *testing.T) {
		token, sa, err := server.mintApprovedToken(capped, &business.DelegationGrant{
			Action:   "read",
			Resource: "repo:codefly-dev/codefly.dev",
		})
		require.NoError(t, err)
		require.NotEmpty(t, token)
		require.NotNil(t, sa)
	})

	t.Run("a resourceless grant carries no kind and is left to the grantor's approval", func(t *testing.T) {
		token, _, err := server.mintApprovedToken(capped, &business.DelegationGrant{
			Action:   "list",
			Resource: "",
		})
		require.NoError(t, err, "the resource-kind ceiling cannot constrain a grant with no resource kind")
		require.NotEmpty(t, token)
	})

	t.Run("an unrestricted agent (empty ceiling) mints any resource kind", func(t *testing.T) {
		open := &business.Principal{
			ID:              "019f6bf7-3333-7333-8333-333333333333",
			Kind:            business.PrincipalKindAgent,
			OrgID:           "019f6bf7-2222-7222-8222-222222222222",
			AgentIdentifier: "publisher/open:1.0.0",
		}
		token, _, err := server.mintApprovedToken(open, &business.DelegationGrant{
			Action:   "read",
			Resource: "secret:prod/api-key",
		})
		require.NoError(t, err)
		require.NotEmpty(t, token)
	})
}
