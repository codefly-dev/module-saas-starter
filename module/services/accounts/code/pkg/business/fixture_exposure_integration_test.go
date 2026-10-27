//go:build !pure

package business_test

import (
	"context"
	"crypto"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"accounts/fixtures"
	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	legacyDeliverySigner     = "legacy-release"
	legacyDeliveryCoordinate = "legacy-host"
)

// legacyCarrier authenticates a module's own delivery workload.
type legacyCarrier struct{}

func (legacyCarrier) AuthorizeCarrier(_ context.Context, credential, namespace string) error {
	if credential != "Bearer delivery:"+namespace {
		return fmt.Errorf("not the delivery workload of %s", namespace)
	}
	return nil
}

// wireSignedDelivery gives the shared service the signed delivery path a
// composed host runs, for the length of one test.
func wireSignedDelivery(t *testing.T) *solutionhost.InMemorySigner {
	t.Helper()
	isolateBindingFixture(t)
	signer := deliverytest.Signer(t, legacyDeliverySigner)
	verifier := deliverytest.Verifier(t, map[string][]string{signer.KeyID(): {"acme"}})
	testService.SetSolutionDelivery(verifier, testStore, legacyCarrier{})
	testService.SetSolutionDeliveryCoordinate(legacyDeliveryCoordinate)
	testService.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	reconciler, err := business.NewSolutionHostBindingReconciler(testService, business.SolutionHostBindingReconcilerConfig{
		Source: business.NewDeliveredSolutionHostBindings(testService), Verifier: verifier, Coordinate: legacyDeliveryCoordinate,
	})
	require.NoError(t, err)
	testService.SetCompositionReconcile(reconciler.RunOnce)
	legacyReconcile = reconciler.RunOnce
	t.Cleanup(func() {
		testService.SetSolutionDelivery(nil, nil, nil)
		testService.SetAuthorityDocumentVerifier(nil)
		testService.SetCompositionReconcile(nil)
	})
	return signer
}

// legacyReconcile is the reconcile pass wireSignedDelivery wired.
var legacyReconcile func(context.Context) error

// deliverSolution applies one signed solution delivery, with its authority
// document carrying roles when roles are given. A solution delivered without
// roles is a legacy delivery the composition interface refuses (a solution
// declares an installer role, the authority core's contract), so it arrives
// through the HTTP delivery endpoint and the reconcile pass, as it did.
func deliverSolution(t *testing.T, signer *solutionhost.InMemorySigner, alias string, generation uint64, roles []*compositionv1.RoleV1) {
	t.Helper()
	m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: alias, Alias: alias, Kind: solutionhost.KindSolution, Generation: generation,
		Host: solutionhost.HostTarget{Coordinate: legacyDeliveryCoordinate, Domain: "acme"}, Release: solutionhost.Release{Publisher: "acme", Name: alias, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "app"}}, Authority: &solutionhost.Authority{Principal: alias, ResourceKinds: []string{"doc"}}}
	raw, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	if roles == nil {
		_, _, err = testService.ReceiveSolutionDelivery(testCtx, deliverytest.Carrier(t, raw, signer.KeyID()), "Bearer delivery:"+alias)
		require.NoError(t, err)
		require.NoError(t, legacyReconcile(testCtx))
		return
	}
	req := &compositionv1.ApplyManifestRequest{Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())}, Credential: "delivery:" + alias}
	if roles != nil {
		doc, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: alias, Generation: generation, CoreManifestDigest: delivery.Digest(raw), Roles: roles,
			ScopeCeilings: []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read", "write"}}}})
		require.NoError(t, err)
		envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
		require.NoError(t, err)
		req.Authority = &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}
	}
	_, err = testService.ApplyComposition(testCtx, req)
	require.NoError(t, err)

}

// The live case: an installation made before the solution's roles were read
// holds the built-in viewer as its standing role, which does not permit using
// a solution. Once the solution delivers its roles, the fixture installer
// exposes it to the team it names with the solution's own viewer, through the
// one exposure rule, and the team's member holds that role's grant.
func TestTheFixtureExposesAnInstallationHoldingTheBuiltInViewerWithTheSolutionsViewer(t *testing.T) {
	clearData(t)
	ctx := testCtx
	signer := wireSignedDelivery(t)
	alias := uniqueAlias("legacy-viewer")
	fixturePath := filepath.Join(t.TempDir(), "legacy.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)
	require.NoError(t, os.WriteFile(fixturePath, []byte(fmt.Sprintf(`users:
  - email: owner@legacy.test
    provider: email
    provider_id: fixture-legacy-owner
  - email: member@legacy.test
    provider: email
    provider_id: fixture-legacy-member
organizations:
  - name: Legacy Org
    owner: owner@legacy.test
    members:
      - email: member@legacy.test
        role: member
teams:
  - name: Finance
    org: Legacy Org
    members: [member@legacy.test]
installations:
  - org: Legacy Org
    target: %s
    role: viewer
    owner: owner@legacy.test
    teams: [Finance]
`, alias)), 0o600))

	// Delivered without roles: installed holding the built-in viewer.
	deliverSolution(t, signer, alias, 1, nil)
	installations, err := fixtures.SeedWithInstallations(ctx, testService, "legacy")
	require.NoError(t, err)
	err = installations.Converge(ctx)
	require.Error(t, err, "a pending installation is not exposed yet")
	owner := seededUUIDFor(t, ctx, "fixture-legacy-owner")
	member := seededUUIDFor(t, ctx, "fixture-legacy-member")
	orgID := seededOrgIDFor(t, ctx, owner, "Legacy Org")
	held := organizationInstallations(t, orgID)
	require.Len(t, held, 1)
	acknowledgeInstallation(t, held[0])
	before, err := testService.ReadHostInstallation(ctx, orgID, held[0].GetId())
	require.NoError(t, err)
	require.Equal(t, "viewer", before.Role)
	require.Equal(t, "e2c1df32-af5b-5ff8-9c5a-afc65532c0b1", before.RoleID, "the built-in viewer")
	err = installations.Converge(ctx)
	require.ErrorContains(t, err, "does not permit solution:use", "the built-in viewer alone exposes nothing")
	require.ErrorContains(t, err, "delivered without its authority document", "and the refusal says why the solution's role was not used")

	// The solution delivers its roles; the next pass exposes the team.
	viewer := []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read"}}}
	deliverSolution(t, signer, alias, 2, []*compositionv1.RoleV1{
		{Name: business.InstallerRole, Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read", "write"}}}},
		{Name: "viewer", Scopes: viewer},
	})
	require.NoError(t, installations.Converge(ctx))
	exposed, err := testService.ReadHostInstallation(ctx, orgID, held[0].GetId())
	require.NoError(t, err)
	require.Len(t, exposed.TeamIDs, 1)
	require.Equal(t, before.RoleID, exposed.RoleID, "the standing grant is not rewritten")

	authority, err := testService.DeliveredSolutionAuthority(ctx, alias)
	require.NoError(t, err)
	require.NotNil(t, authority)
	grants := authority.HeldBy(member, exposed.TeamIDs, business.SolutionRoleHolders{Owner: owner, Role: exposed.Role, Teams: exposed.TeamIDs})
	require.Equal(t, authority.Grants("viewer"), grants)
	require.NoError(t, installations.Converge(ctx), "converged: nothing left to expose")
	_ = gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE
}
