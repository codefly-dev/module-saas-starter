//go:build !pure

package business_test

import (
	"cmp"
	"context"
	"crypto"
	"crypto/ed25519"
	"fmt"
	"slices"
	"testing"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// approvedLine is a solution delivered through the signed delivery path at
// generations 1 and 2, generation 2 adding an editor role, and installations
// whose consent approved generation 1, recorded before the store kept
// approved declarations: the state migration 68 leaves a consent that
// approved an older generation in. Its owner holds viewer and editor on each.
type approvedLine struct {
	alias  string
	signer *solutionhost.InMemorySigner
	// approved is the offer digest of generation 1 as the core composed it:
	// the one an administrator's review of it recorded.
	approved string
	// holders are the installations, one per organization, and their owner.
	holders []lineHolder
}

type lineHolder struct{ installation, org, owner string }

// newApprovedLine delivers the line and records one installation per digest
// given, each at its own organization and consenting to generation 1 under
// that digest ("" is the one generation 1 composes to).
func newApprovedLine(t *testing.T, digests ...string) approvedLine {
	t.Helper()
	clearData(t)
	withAuthorityCore(t)
	line := approvedLine{alias: uniqueAlias("approved-line"), signer: wireSignedDelivery(t)}
	doc := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: actions}}
	}
	roles := []*compositionv1.RoleV1{{Name: business.InstallerRole, Scopes: doc("read", "write")}, {Name: "viewer", Scopes: doc("read")}}
	line.deliver(t, 1, roles)
	core, err := testService.AuthorityCore()
	require.NoError(t, err)
	first, err := core.Composed(testCtx, line.alias)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Generation)
	line.approved, err = authority.OfferDigest(first)
	require.NoError(t, err)
	line.deliver(t, 2, append(roles, &compositionv1.RoleV1{Name: "editor", Scopes: doc("read", "write")}))

	for i, digest := range digests {
		name := fmt.Sprintf("%s-%d", line.alias, i)
		owner, org := mustUserAndOrg(t, testCtx, name+"@line.test", name, "Line "+name)
		inst := domain.Installation{ID: business.NewIDString(), Organization: org, Target: line.alias, Status: domain.Active, Revision: 1,
			AgentRole: domain.NoInstallationRole, PrincipalID: business.NewIDString(), InstalledBy: owner,
			Consent: domain.ConsentReceipt{OperationID: business.NewIDString(), Intent: "install", Revision: 1, Generation: 1, Digest: cmp.Or(digest, line.approved)}}
		require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
			if err := tx.PutInstallation(inst); err != nil {
				return err
			}
			for _, role := range []string{"viewer", "editor"} {
				if err := tx.PutRoleGrant(domain.RoleGrant{InstallationID: inst.ID, Role: role, PersonID: owner}); err != nil {
					return err
				}
			}
			return nil
		}))
		line.holders = append(line.holders, lineHolder{installation: inst.ID, org: org, owner: owner})
		t.Cleanup(func() {
			inst.Status = domain.Revoked
			_ = testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutInstallation(inst) })
		})
	}
	return line
}

// deliver applies one owner-aware generation of the line through the
// composition interface, signed by the line's release key.
func (line approvedLine) deliver(t *testing.T, generation uint64, roles []*compositionv1.RoleV1, also ...func(*compositionv1.AuthorityManifestV1)) {
	t.Helper()
	m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: line.alias, Alias: line.alias, Kind: solutionhost.KindSolution, Generation: generation,
		Host: solutionhost.HostTarget{Coordinate: legacyDeliveryCoordinate, Domain: "acme"}, Release: solutionhost.Release{Publisher: "acme", Name: line.alias, Version: fmt.Sprintf("1.%d.0", generation)},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "app"}}, Authority: &solutionhost.Authority{Principal: line.alias, ResourceKinds: []string{"doc"}}}
	raw, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	document := &compositionv1.AuthorityManifestV1{Module: line.alias, Generation: generation, CoreManifestDigest: delivery.Digest(raw), Roles: roles, OwnerAware: true,
		ScopeCeilings: []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read", "write"}}}}
	for _, f := range also {
		f(document)
	}
	doc, err := proto.Marshal(document)
	require.NoError(t, err)
	envelope, err := delivery.Sign(doc, line.signer.KeyID(), line.signer.Sign)
	require.NoError(t, err)
	_, err = testService.ApplyComposition(testCtx, &compositionv1.ApplyManifestRequest{
		Carrier:    &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, line.signer.KeyID())},
		Credential: "delivery:" + line.alias, Authority: &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}})
	require.NoError(t, err)
}

// hidingInbox is the shared delivery inbox with some of the line's received
// generations not arrived yet.
type hidingInbox struct {
	*infra.PostgresStore
	binding string
	hidden  map[uint64]bool
}

func (i hidingInbox) ReadDeliveredDocument(ctx context.Context, binding string, generation uint64) (*business.SolutionDeliveryRecord, error) {
	if binding == i.binding && i.hidden[generation] {
		return nil, nil
	}
	return i.PostgresStore.ReadDeliveredDocument(ctx, binding, generation)
}

// boot configures another host's authority core over the shared store, as
// accounts does at start, pinning keys's release key and reading inbox.
func (line approvedLine) boot(t *testing.T, keys *solutionhost.InMemorySigner, inbox business.SolutionDeliveryStore) *business.Service {
	t.Helper()
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetSolutionDelivery(deliverytest.Verifier(t, map[string][]string{keys.KeyID(): {"acme"}}), inbox, legacyCarrier{})
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{keys.KeyID(): keys.Public()})
	})
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_ = svc.ConfigureAuthorityCore(testCtx, "https://accounts.example.test", "approved-line", key)
	_, err = svc.AuthorityCore()
	require.NoError(t, err, "the host serves whatever its reconciliation answered")
	return svc
}

// kept is the declaration the store keeps for the line's generation 1, if any.
func (line approvedLine) kept(t *testing.T) (domain.Module, bool) {
	t.Helper()
	var approved domain.Module
	err := testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		var err error
		approved, err = tx.ApprovedModule(line.alias, 1, "")
		return err
	})
	if err != nil {
		require.ErrorIs(t, err, domain.ErrNotFound)
		return domain.Module{}, false
	}
	return approved, true
}

// held is one holder's installation as the core reads it, and its target as
// composed now.
func (line approvedLine) held(t *testing.T, core *authority.Service, h lineHolder) (domain.Installation, domain.Module) {
	t.Helper()
	all, err := core.Installations(testCtx, authority.Person{ID: h.owner, Organization: h.org}, h.org)
	require.NoError(t, err)
	i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.ID == h.installation })
	require.GreaterOrEqual(t, i, 0)
	composed, err := core.Composed(testCtx, line.alias)
	require.NoError(t, err)
	require.Equal(t, uint64(2), composed.Generation)
	return all[i], composed
}

// issue is a Work Context for one holder's owner at the line's target.
func (line approvedLine) issue(core *authority.Service, h lineHolder) (authority.Issued, error) {
	return core.IssueForPerson(testCtx, authority.Person{ID: h.owner, Organization: h.org}, authority.IssueRequest{Audience: line.alias})
}

// A consent that approved an older generation is held to that generation's
// declaration, rebuilt on a delivery pass from the documents the inbox
// received for it, verified again and the offer the consent approved: its
// holders do not hold the role a later delivery added, every host lists what
// they hold from the core's own rule, and the core offers the upgrade. Another
// host booting writes nothing new.
func TestADeliveryPassKeepsTheDeclarationAnOlderConsentApproved(t *testing.T) {
	line := newApprovedLine(t, "")
	require.NoError(t, legacyReconcile(testCtx))
	approved, kept := line.kept(t)
	require.True(t, kept, "the pass writes the declaration generation 1 composes to")
	require.NotContains(t, approved.Roles, "editor")
	require.Contains(t, approved.Roles, "viewer")
	digest, err := authority.OfferDigest(approved)
	require.NoError(t, err)
	require.Equal(t, line.approved, digest, "exactly the offer the consent approved")

	core, err := testService.AuthorityCore()
	require.NoError(t, err)
	inst, composed := line.held(t, core, line.holders[0])
	require.True(t, decide.UpgradeAvailable(inst, composed))
	issued, err := line.issue(core, line.holders[0])
	require.NoError(t, err, "the holder holds what generation 1 granted")
	require.NotEmpty(t, issued.Context.GetAuthorityScopes())
	for _, sc := range issued.Context.GetAuthorityScopes() {
		require.NotContains(t, sc.GetActions(), "write", "editor, added at generation 2, was never approved")
	}
	listed, err := core.ApprovedDeclaration(testCtx, inst)
	require.NoError(t, err)
	require.Equal(t, approved, listed, "what a holder may hold is listed from the declaration the consent approved")

	line.boot(t, line.signer, testStore) // idempotent
	again, kept := line.kept(t)
	require.True(t, kept)
	require.Equal(t, approved, again)
}

// Documents that no longer verify against the pinned keys rebuild nothing: a
// host pinning another release key writes no declaration at boot, and the
// core refuses issuance through the installation by name. A later pass of a
// host that verifies them writes it.
func TestAnApprovedDeclarationIsRebuiltOnlyFromDocumentsThatVerify(t *testing.T) {
	line := newApprovedLine(t, "")
	rotated := line.boot(t, deliverytest.Signer(t, "rotated-release"), testStore)
	_, kept := line.kept(t)
	require.False(t, kept, "a document that does not verify rebuilds nothing")
	core, err := rotated.AuthorityCore()
	require.NoError(t, err)
	_, err = line.issue(core, line.holders[0])
	require.ErrorContains(t, err, "the generation 1 of "+line.alias+" that installation "+line.holders[0].installation+" approved is not kept")

	require.NoError(t, legacyReconcile(testCtx))
	_, kept = line.kept(t)
	require.True(t, kept, "a later pass that verifies the documents writes it")
}

// A generation whose documents have not arrived is written by the first pass
// after they do.
func TestAnApprovedDeclarationIsWrittenOnceItsDocumentsArrive(t *testing.T) {
	line := newApprovedLine(t, "")
	inbox := hidingInbox{PostgresStore: testStore, binding: line.alias, hidden: map[uint64]bool{1: true}}
	svc := line.boot(t, line.signer, inbox)
	_, kept := line.kept(t)
	require.False(t, kept, "nothing is written for documents the inbox does not hold")
	core, err := svc.AuthorityCore()
	require.NoError(t, err)
	inst, _ := line.held(t, core, line.holders[0])
	_, err = core.ApprovedDeclaration(testCtx, inst)
	require.ErrorContains(t, err, "approved is not kept", "no role is listed for an installation whose approved declaration is not kept")

	delete(inbox.hidden, 1)
	require.NoError(t, svc.KeepAuthorityCoreCurrent(testCtx))
	_, kept = line.kept(t)
	require.True(t, kept)
	_, err = line.issue(core, line.holders[0])
	require.NoError(t, err)
}

// A rebuilt declaration is written only for the consents that approved it:
// approved declarations are kept by offer digest, so the consent to the offer
// the inbox's documents compose is kept and its holder issued, and one whose
// consent recorded another digest gets nothing and is refused issuance by
// name, never served the other's.
func TestAnApprovedDeclarationIsWrittenOnlyForTheConsentsThatApprovedIt(t *testing.T) {
	line := newApprovedLine(t, "", "sha256:another-offer")
	require.NoError(t, legacyReconcile(testCtx))
	approved, kept := line.kept(t)
	require.True(t, kept, "the consent to the composed offer is kept")
	digest, err := authority.OfferDigest(approved)
	require.NoError(t, err)
	require.Equal(t, line.approved, digest)
	core, err := testService.AuthorityCore()
	require.NoError(t, err)
	_, err = line.issue(core, line.holders[0])
	require.NoError(t, err)
	_, err = line.issue(core, line.holders[1])
	require.ErrorContains(t, err, "kept for installation "+line.holders[1].installation+" is not the offer it approved")
}
