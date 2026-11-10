package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// A listing's upgrade_available answers are kept for one composition only: a
// delivery or withdrawal of any module names another composition, and every
// answer is computed again.
func TestUpgradeAnswersAreKeptPerComposition(t *testing.T) {
	first := compositionStamp([]domain.Module{{Name: "b", Generation: 1}, {Name: "a", Generation: 2}})
	require.Equal(t, first, compositionStamp([]domain.Module{{Name: "a", Generation: 2}, {Name: "b", Generation: 1}}), "the order of the read does not matter")
	redelivered := compositionStamp([]domain.Module{{Name: "a", Generation: 3}, {Name: "b", Generation: 1}})
	withdrawn := compositionStamp([]domain.Module{{Name: "a", Generation: 2}})
	require.NotEqual(t, first, redelivered)
	require.NotEqual(t, first, withdrawn)

	var cache upgradeCache
	_, ok := cache.get(first, "inst@1@1@sha256:r")
	require.False(t, ok)
	cache.put(first, "inst@1@1@sha256:r", true)
	answer, ok := cache.get(first, "inst@1@1@sha256:r")
	require.True(t, ok)
	require.True(t, answer)
	_, ok = cache.get(redelivered, "inst@1@1@sha256:r")
	require.False(t, ok, "another composition computes it again")
	cache.put(redelivered, "inst@1@1@sha256:r", false)
	_, ok = cache.get(first, "inst@1@1@sha256:r")
	require.False(t, ok, "the earlier composition's answers are dropped")
}
