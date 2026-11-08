//go:build !pure

package business_test

import (
	"context"
	"crypto/ed25519"
	"slices"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// approvedLine is a solution delivered at generations 1 and 2, generation 2
// adding an editor role, and an installation whose consent approved
// generation 1 recorded before the store kept approved declarations: the
// state migration 68 leaves a consent that approved an older generation in.
// Its owner is granted viewer and editor.
type approvedLine struct {
	alias, org, owner string
	installation      string
}

// newApprovedLine seeds the line; the inbox keeps generation 1's documents
// only when keepFirst is set (a delivery history with a gap otherwise).
func newApprovedLine(t *testing.T, keepFirst bool) approvedLine {
	t.Helper()
	clearData(t)
	line := approvedLine{alias: uniqueAlias("approved-line")}
	line.owner, line.org = mustUserAndOrg(t, testCtx, line.alias+"@line.test", line.alias, "Line "+line.alias)
	binding := "acme.test." + line.alias
	kind := line.alias + ".page"
	scopes := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: kind, Actions: actions}}
	}
	roles := []*compositionv1.RoleV1{{Name: business.InstallerRole, Scopes: scopes("read", "write")}, {Name: "viewer", Scopes: scopes("read")}}
	now := time.Now().UTC()
	signer := deliverytest.Signer(t, testSignerIdentity)
	var applied *solutionhost.ModuleManifest
	var appliedPayload []byte
	for generation := uint64(1); generation <= 2; generation++ {
		manifest := declaredBinding(t, line.alias, generation)
		manifest.Authority = &solutionhost.Authority{Principal: line.alias, ResourceKinds: []string{kind}}
		if generation == 2 {
			roles = append(roles, &compositionv1.RoleV1{Name: "editor", Scopes: scopes("read", "write")})
		}
		payload, err := solutionhost.SignedPayload(manifest)
		require.NoError(t, err)
		digest, err := manifest.Digest()
		require.NoError(t, err)
		document, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: line.alias, Generation: generation,
			CoreManifestDigest: delivery.Digest(payload), OwnerAware: true, Roles: roles})
		require.NoError(t, err)
		envelope, err := delivery.Sign(document, signer.KeyID(), signer.Sign)
		require.NoError(t, err)
		applied, appliedPayload = manifest, payload
		if generation == 1 && !keepFirst {
			continue
		}
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, err := testStore.RecordDeliveredDocument(ctx, &business.SolutionDeliveryRecord{Binding: binding, Generation: generation, ContentHash: digest,
				Payload: payload, Envelope: deliverytest.Envelope(t, payload, testSignerIdentity), AuthorityDocument: document, AuthorityEnvelope: envelope,
				SignerIdentity: testSignerIdentity, OwnershipDomain: testOwnershipDomain, ReceivedAt: now})
			return err
		}))
	}
	// The binding stands at generation 2, which the core composes at boot.
	digest, err := applied.Digest()
	require.NoError(t, err)
	entry := business.SolutionHostBindingGeneration{Generation: 2, Digest: digest, Document: string(appliedPayload), At: now}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.SaveSolutionHostBinding(ctx, &business.SolutionHostBindingRecord{BindingID: binding, HostCoordinate: testHostCoordinate,
			Desired: &entry, Applied: &business.SolutionHostBindingApplied{SolutionHostBindingGeneration: entry, SolutionID: line.alias,
				Routes: []string{line.alias}, Domain: testOwnershipDomain, Release: applied.Release.Identity()}, UpdatedAt: now})
	}))
	line.installation = business.NewIDString()
	inst := domain.Installation{ID: line.installation, Organization: line.org, Target: line.alias, Status: domain.Active, Revision: 1,
		AgentRole: domain.NoInstallationRole, PrincipalID: business.NewIDString(), InstalledBy: line.owner,
		Consent: domain.ConsentReceipt{OperationID: business.NewIDString(), Intent: "install", Revision: 1, Generation: 1, Digest: fixtureDigest(line.alias)}}
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
		if err := tx.PutInstallation(inst); err != nil {
			return err
		}
		for _, role := range []string{"viewer", "editor"} {
			if err := tx.PutRoleGrant(domain.RoleGrant{InstallationID: inst.ID, Role: role, PersonID: line.owner}); err != nil {
				return err
			}
		}
		return nil
	}))
	t.Cleanup(func() {
		inst.Status = domain.Revoked
		_ = testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutInstallation(inst) })
	})
	return line
}

// boot configures a host's authority core over the shared store as accounts
// does at start, with the delivery inbox it reads.
func (line approvedLine) boot(t *testing.T) (*business.Service, *authority.Service) {
	t.Helper()
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetSolutionDelivery(nil, testStore, nil)
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, svc.ConfigureAuthorityCore(testCtx, "https://accounts.example.test", "approved-line", key))
	core, err := svc.AuthorityCore()
	require.NoError(t, err)
	return svc, core
}

// held is the line's installation as the core reads it, and its target as
// composed now.
func (line approvedLine) held(t *testing.T, core *authority.Service) (domain.Installation, domain.Module) {
	t.Helper()
	all, err := core.Installations(testCtx, authority.Person{ID: line.owner, Organization: line.org}, line.org)
	require.NoError(t, err)
	i := slices.IndexFunc(all, func(inst domain.Installation) bool { return inst.ID == line.installation })
	require.GreaterOrEqual(t, i, 0)
	composed, err := core.Composed(testCtx, line.alias)
	require.NoError(t, err)
	require.Equal(t, uint64(2), composed.Generation)
	return all[i], composed
}

// A consent that approved an older generation is held to that generation's
// declaration, rebuilt at boot from the documents the inbox kept for it: its
// holders do not hold the role a later delivery added, and the core offers the
// upgrade an administrator approves. Booting again writes nothing new.
func TestBootKeepsTheDeclarationAnOlderConsentApproved(t *testing.T) {
	line := newApprovedLine(t, true)
	svc, core := line.boot(t)
	inst, composed := line.held(t, core)
	require.True(t, decide.UpgradeAvailable(inst, composed))
	issued, err := core.IssueForPerson(testCtx, authority.Person{ID: line.owner, Organization: line.org}, authority.IssueRequest{Audience: line.alias})
	require.NoError(t, err, "the holder holds what generation 1 granted")
	require.NotEmpty(t, issued.Context.GetAuthorityScopes())
	for _, sc := range issued.Context.GetAuthorityScopes() {
		require.NotContains(t, sc.GetActions(), "write", "editor, added at generation 2, was never approved")
	}
	var approved domain.Module
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		approved, err = tx.ApprovedModule(line.alias, 1)
		return err
	}))
	require.NotContains(t, approved.Roles, "editor")
	require.Contains(t, approved.Roles, "viewer")
	listed, err := svc.ApprovedDeclaration(testCtx, inst, composed)
	require.NoError(t, err)
	require.Equal(t, approved, listed, "what a holder may hold is listed from the declaration the consent approved")

	line.boot(t) // idempotent
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		again, err := tx.ApprovedModule(line.alias, 1)
		require.Equal(t, approved, again)
		return err
	}))
}

// Where the inbox kept no document for the approved generation, nothing is
// written: the core refuses issuance through the installation by name until
// an administrator upgrades it.
func TestBootWritesNothingForAnApprovedGenerationTheInboxLost(t *testing.T) {
	line := newApprovedLine(t, false)
	svc, core := line.boot(t)
	inst, composed := line.held(t, core)
	require.True(t, decide.UpgradeAvailable(inst, composed))
	_, err := svc.ApprovedDeclaration(testCtx, inst, composed)
	require.ErrorIs(t, err, domain.ErrNotFound, "no role is listed for an installation whose approved declaration is lost")
	_, err = core.IssueForPerson(testCtx, authority.Person{ID: line.owner, Organization: line.org}, authority.IssueRequest{Audience: line.alias})
	require.ErrorContains(t, err, "the generation 1 of "+line.alias+" that installation "+line.installation+" approved is not kept")
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		_, err := tx.ApprovedModule(line.alias, 1)
		require.ErrorIs(t, err, domain.ErrNotFound)
		return nil
	}))
}
