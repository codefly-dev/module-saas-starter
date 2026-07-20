package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These are pure-Go unit tests for Principal. Postgres-dependent
// behavior lives in pkg/infra/postgres_principals_test.go.

func TestPrincipal_Validate(t *testing.T) {
	tests := []struct {
		name      string
		p         *Principal
		wantError bool
		errSubstr string
	}{
		{
			name:      "nil rejected",
			p:         nil,
			wantError: true,
			errSubstr: "nil",
		},
		{
			name:      "empty ID rejected",
			p:         &Principal{Kind: PrincipalKindHuman, DisplayName: "n"},
			wantError: true,
			errSubstr: "empty ID",
		},
		{
			name:      "empty DisplayName rejected",
			p:         &Principal{ID: "id-1", Kind: PrincipalKindHuman},
			wantError: true,
			errSubstr: "empty DisplayName",
		},
		{
			name:      "empty Kind rejected",
			p:         &Principal{ID: "id-1", DisplayName: "n"},
			wantError: true,
			errSubstr: "empty Kind",
		},
		{
			name:      "unknown Kind rejected",
			p:         &Principal{ID: "id-1", Kind: "robot", DisplayName: "n"},
			wantError: true,
			errSubstr: "unknown Kind",
		},
		{
			name: "human with OrgID rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindHuman, DisplayName: "n",
				OrgID: "org-1",
			},
			wantError: true,
			errSubstr: "cross-org",
		},
		{
			name: "human with AgentIdentifier rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindHuman, DisplayName: "n",
				AgentIdentifier: "x/y:z",
			},
			wantError: true,
			errSubstr: "AgentIdentifier",
		},
		{
			name: "service without OrgID rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindService, DisplayName: "n",
			},
			wantError: true,
			errSubstr: "OrgID",
		},
		{
			name: "service with AgentIdentifier rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindService, DisplayName: "n",
				OrgID: "org-1", AgentIdentifier: "x/y:z",
			},
			wantError: true,
			errSubstr: "AgentIdentifier",
		},
		{
			name: "agent without OrgID rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindAgent, DisplayName: "n",
				AgentIdentifier: "publisher/name:0.0.1",
			},
			wantError: true,
			errSubstr: "OrgID",
		},
		{
			name: "agent without AgentIdentifier rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindAgent, DisplayName: "n",
				OrgID: "org-1",
			},
			wantError: true,
			errSubstr: "AgentIdentifier",
		},
		{
			name: "agent with malformed AgentIdentifier rejected",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindAgent, DisplayName: "n",
				OrgID: "org-1", AgentIdentifier: "not-canonical",
			},
			wantError: true,
			errSubstr: "publisher/name:version",
		},
		{
			name: "valid human",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindHuman, DisplayName: "antoine",
			},
		},
		{
			name: "valid service",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindService, DisplayName: "ci",
				OrgID: "org-1",
			},
		},
		{
			name: "valid agent",
			p: &Principal{
				ID: "id-1", Kind: PrincipalKindAgent, DisplayName: "Auto Merge",
				OrgID: "org-1", AgentIdentifier: "codefly.dev/auto-merge:0.1.0",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if tc.wantError {
				require.Error(t, err)
				if tc.errSubstr != "" {
					require.Contains(t, err.Error(), tc.errSubstr)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestPrincipal_IsRevoked(t *testing.T) {
	t.Run("nil principal not revoked", func(t *testing.T) {
		var p *Principal
		require.False(t, p.IsRevoked())
	})

	t.Run("zero RevokedAt not revoked", func(t *testing.T) {
		p := &Principal{ID: "x"}
		require.False(t, p.IsRevoked())
	})

	t.Run("non-nil RevokedAt is revoked", func(t *testing.T) {
		now := time.Now()
		p := &Principal{ID: "x", RevokedAt: &now}
		require.True(t, p.IsRevoked())
	})
}

func TestPrincipal_LooksLikeAgentIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"codefly.dev/auto-merge:0.1.0", true},
		{"a/b:c", true},
		{"publisher/name:version", true},
		// Malformed cases:
		{"", false},
		{"publisher/name", false},    // no colon
		{"publisher:version", false}, // no slash
		{"publisher/name:", false},   // empty version
		{"/name:version", false},     // empty publisher
		{"name:version", false},      // no slash
		{":version", false},          // no slash before colon
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			require.Equal(t, tc.want, looksLikeAgentIdentifier(tc.input),
				"input=%q", tc.input)
		})
	}
}
