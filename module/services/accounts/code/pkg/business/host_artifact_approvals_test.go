package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostArtifactConsentStaysWithItsPolicy(t *testing.T) {
	store := &artifactDecisionStore{rows: map[string]*ExecutableArtifactApproval{}}
	svc, err := NewService(store)
	require.NoError(t, err)
	ctx := context.Background()
	subject := []byte("canonical identity")
	approve := func(policy string) (*HostArtifactApproval, error) {
		return svc.ApproveHostArtifact(ctx, "org", "person", "installation", "module", policy, "digest", subject, 3)
	}
	authorize := func(policy string) (*HostArtifactApproval, error) {
		return svc.AuthorizedHostArtifact(ctx, "org", "person", "installation", "module", policy, "digest", subject, 3)
	}
	revoke := func(policy, id string) (*HostArtifactApproval, error) {
		return svc.RevokeHostArtifact(ctx, "org", "person", "installation", "module", policy, id, func([]byte) int64 { return 3 })
	}
	first, err := approve("example.workflow")
	require.NoError(t, err)
	_, err = authorize("example.preview")
	require.ErrorContains(t, err, "no standing approval")
	_, err = revoke("example.preview", first.ID)
	require.ErrorContains(t, err, "no standing approval")
	same, err := approve("example.workflow")
	require.NoError(t, err)
	require.Equal(t, first, same)
	second, err := approve("example.preview")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	_, err = revoke("example.workflow", first.ID)
	require.NoError(t, err)
	_, err = authorize("example.workflow")
	require.ErrorContains(t, err, "no standing approval")
	_, err = approve("example.workflow")
	require.ErrorContains(t, err, "no standing approval")
	got, err := authorize("example.preview")
	require.NoError(t, err)
	require.Equal(t, second, got)
}
