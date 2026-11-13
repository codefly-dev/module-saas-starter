//go:build !pure

package business_test

import (
	"strings"
	"sync"
	"testing"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
)

// A host that learns to read a field of the authority document reads, at its
// start, every generation the core holds again from the inbox's verified
// record of it: a generation an older host composed without the field is
// re-read in place, not refused as a changed declaration, and that is logged
// once, since the next start reads the same declaration.
func TestABootReadsAGenerationAnOlderHostComposedWithoutAFieldAgainOnce(t *testing.T) {
	clearData(t)
	withAuthorityCore(t)
	line := approvedLine{alias: uniqueAlias("reread-line"), signer: wireSignedDelivery(t)}
	roles := []*compositionv1.RoleV1{{Name: business.InstallerRole, Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read", "write"}}}}}
	line.deliver(t, 1, roles, func(doc *compositionv1.AuthorityManifestV1) { doc.SharedKinds = []string{"doc"} })

	// As the older host composed it: the shared kinds unread, no source kept.
	var delivered string
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
		m, err := tx.ModuleNamed(line.alias)
		if err != nil {
			return err
		}
		delivered = m.Source
		m.SharedKinds, m.Source = nil, ""
		return tx.PutModule(m)
	}))
	logs := captureReread(t)
	for range 2 {
		svc := line.boot(t, line.signer, testStore)
		core, err := svc.AuthorityCore()
		require.NoError(t, err)
		held, err := core.Composed(testCtx, line.alias)
		require.NoError(t, err)
		require.Equal(t, uint64(1), held.Generation)
		require.Equal(t, []string{"doc"}, held.SharedKinds, "the held generation is read again with the field")
		require.NotEmpty(t, delivered, "the delivery records the source it was read from")
		require.Equal(t, delivered, held.Source, "the re-read reads the inbox's record of the very delivery composed")
	}
	require.Equal(t, 1, strings.Count(logs(), "read a held generation again"), "the re-read is logged once: %s", logs())
	require.Contains(t, logs(), line.alias)
}

// captureReread captures what wool's fallback logger logs during the test.
func captureReread(t *testing.T) func() string {
	t.Helper()
	capture := &rereadLogs{}
	wool.SetFallbackLogger(capture)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	return capture.String
}

type rereadLogs struct {
	mu    sync.Mutex
	lines []string
}

func (c *rereadLogs) Process(msg *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, msg.String())
}

func (c *rereadLogs) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}
