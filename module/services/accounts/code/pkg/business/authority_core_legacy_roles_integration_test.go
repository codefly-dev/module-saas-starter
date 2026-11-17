//go:build !pure

package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	coredelivered "github.com/codefly-dev/authority/libraries/authority-core/go/delivered"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// legacyRolesInbox answers the line's generation with its authority document
// replaced by one declaring old roles and signed by the line's key: the
// delivery an older host composed when only core's render refused old roles.
type legacyRolesInbox struct {
	*infra.PostgresStore
	binding            string
	document, envelope []byte
}

func (i legacyRolesInbox) ReadDeliveredDocument(ctx context.Context, binding string, generation uint64) (*business.SolutionDeliveryRecord, error) {
	record, err := i.PostgresStore.ReadDeliveredDocument(ctx, binding, generation)
	if err != nil || record == nil || binding != i.binding {
		return record, err
	}
	legacy := *record
	legacy.AuthorityDocument, legacy.AuthorityEnvelope = i.document, i.envelope
	return &legacy, nil
}

// legacyInstallerRole is the name a delivery composed before the rename gave
// its admin role. The core no longer names it: admin and user are the only
// roles. It is spelled here because the delivery this test replays carries it.
const legacyInstallerRole = "installer"

// A host holding a generation that declares old roles (installer, editor,
// viewer) reads it again at boot and composes it as it was taken: only a NEW
// generation is held to admin and user (decide.PageRoles), so the boot
// reconciliation completes, and the held generation's next delivery moves the
// old roles' holders.
func TestABootReadsAHeldGenerationDeclaringOldRolesAgain(t *testing.T) {
	clearData(t)
	withAuthorityCore(t)
	line := approvedLine{alias: uniqueAlias("legacy-roles"), signer: wireSignedDelivery(t)}
	scopes := []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read", "write"}}}
	line.deliver(t, 1, []*compositionv1.RoleV1{{Name: domain.AdminRole, Scopes: scopes}, {Name: domain.UserRole, Scopes: scopes}})

	var record *business.SolutionDeliveryRecord
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		record, err = testStore.ReadDeliveredDocument(ctx, line.alias, 1)
		return err
	}))
	require.NotNil(t, record)
	var wire compositionv1.AuthorityManifestV1
	require.NoError(t, proto.Unmarshal(record.AuthorityDocument, &wire))
	wire.Roles = []*compositionv1.RoleV1{{Name: legacyInstallerRole, Scopes: scopes}, {Name: "editor", Scopes: scopes},
		{Name: "viewer", Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read"}}}}}
	document, err := proto.Marshal(&wire)
	require.NoError(t, err)
	envelope, err := delivery.Sign(document, line.signer.KeyID(), line.signer.Sign)
	require.NoError(t, err)

	// As the older host composed it: the old roles, read from that delivery.
	// Put back as delivered afterwards, so a later pass over the shared inbox
	// meets what it holds.
	var original domain.Module
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
		m, err := tx.ModuleNamed(line.alias)
		if err != nil {
			return err
		}
		original = m
		doc := func(actions ...string) []*basev0.WorkScopeV1 {
			return []*basev0.WorkScopeV1{{ResourceKind: "doc", Actions: actions}}
		}
		m.Roles = map[string][]*basev0.WorkScopeV1{legacyInstallerRole: doc("read", "write"), "editor": doc("read", "write"), "viewer": doc("read")}
		m.Source = coredelivered.Source(record.Payload, document)
		return tx.PutModule(m)
	}))
	t.Cleanup(func() {
		_ = testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutModule(original) })
	})

	svc := line.boot(t, line.signer, legacyRolesInbox{PostgresStore: testStore, binding: line.alias, document: document, envelope: envelope})
	require.NoError(t, svc.ReconcileHeldDeliveries(testCtx), "the held generation declaring old roles is read again, not refused")
	core, err := svc.AuthorityCore()
	require.NoError(t, err)
	held, err := core.Composed(testCtx, line.alias)
	require.NoError(t, err)
	require.Equal(t, uint64(1), held.Generation)
	require.Contains(t, held.Roles, "editor", "the held generation is composed as it was taken")
}
