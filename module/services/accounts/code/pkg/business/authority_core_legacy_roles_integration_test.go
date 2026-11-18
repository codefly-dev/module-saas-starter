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

// Role names are admin and user only, so the core refuses to read again a
// held generation that declares any other (installer, editor, viewer). A host
// holding one does not fail its boot on it, start after start: the boot
// reconciliation completes, reports the binding by name as needing
// redelivery, on the binding's own row where an operator reads a delivery
// this host could not take, and composes nothing new from it. Until its
// publisher delivers the two roles the solution is unavailable: what the
// store holds under another name holds nothing.
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
	require.NoError(t, svc.ReconcileHeldDeliveries(testCtx), "a held generation under old role names fails no boot")
	reason := svc.RedeliveryReasons()[line.alias]
	require.Contains(t, reason, "needs redelivery", "the binding is reported by name as needing redelivery")
	require.Contains(t, reason, `"editor"`, "naming the role the core refuses")
	require.Contains(t, reason, line.alias)
	var row *business.SolutionHostBindingRecord
	records, err := svc.ListSolutionHostBindings(testCtx)
	require.NoError(t, err)
	for _, record := range records {
		if record.BindingID == line.alias {
			row = record
		}
	}
	require.NotNil(t, row)
	require.Equal(t, reason, row.PendingReason, "on the binding's own row, beside the generation it holds")
	require.NotNil(t, row.PendingSince)

	// Unavailable until redelivered: the core still holds generation 1 as it
	// was taken, and a role under an old name holds nothing.
	core, err := svc.AuthorityCore()
	require.NoError(t, err)
	held, err := core.Composed(testCtx, line.alias)
	require.NoError(t, err)
	require.Equal(t, uint64(1), held.Generation)
	for name := range held.Roles {
		require.False(t, domain.IsRole(name), "role %q is one the core grants nothing by", name)
	}

	// The reason stands across passes (a second start reads the same), and is
	// not an error of the pass.
	require.NoError(t, svc.ReconcileHeldDeliveries(testCtx))
	require.Equal(t, reason, svc.RedeliveryReasons()[line.alias])
}
