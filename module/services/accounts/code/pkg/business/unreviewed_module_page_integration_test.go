//go:build !pure

package business_test

import (
	"context"
	"crypto/ed25519"
	"slices"
	"testing"

	"accounts/internal/deliverytest"
	"accounts/pkg/adapters"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// needsReview is the condition the installation read states for
// installation, if any: the record is not healthy, and an organization
// administrator clears it.
func needsReview(t *testing.T, admin, org, installation string) (*gen.ReadinessCondition, bool) {
	t.Helper()
	adapters.WithService(testService)
	listed, err := (&adapters.InstallationServer{}).ListOrganizationInstallations(storyCallerContext(testCtx, admin, org),
		&gen.ListOrganizationInstallationsRequest{OrgId: org})
	require.NoError(t, err)
	rows := listed.GetInstallations()
	i := slices.IndexFunc(rows, func(row *gen.OrganizationInstallation) bool {
		return row.GetInstallation().GetId() == installation
	})
	require.GreaterOrEqual(t, i, 0, "the installation is listed")
	for _, c := range rows[i].GetReadiness().GetUnmet() {
		if c.GetKind() == gen.ReadinessConditionKind_READINESS_CONDITION_KIND_RECORD_UNHEALTHY && c.GetClearedBy() == gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN {
			require.False(t, rows[i].GetReadiness().GetReady())
			return c, true
		}
	}
	return nil, false
}

// A module installed by name before it declared roles never stops the host:
// boot configures the core and serves, and the installation read states that
// it needs its review, who clears it (an organization administrator) and how
// (uninstall it, then install it through its review). Once uninstalled, it
// reads so no longer.
func TestAModulePageInstalledByNameReadsAsNeedingItsReview(t *testing.T) {
	clearData(t)
	admin, org := mustUserAndOrg(t, testCtx, "admin@page-by-name.test", "page-by-name", "Page By Name")
	installation, _, alias := installForExposure(t, admin, org)
	page := func(generation uint64, roles map[string][]*basev0.WorkScopeV1) {
		t.Helper()
		require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error {
			return tx.PutModule(domain.Module{Name: alias, Principal: alias, Kind: domain.KindModule, Generation: generation, Roles: roles})
		}))
	}
	_, flagged := needsReview(t, admin, org, installation)
	require.False(t, flagged, "a module declaring no roles needs no review")

	page(2, map[string][]*basev0.WorkScopeV1{"viewer": {{ResourceKind: alias + ".note", Actions: []string{"read"}}}})
	t.Cleanup(func() { page(3, nil) })
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, svc.ConfigureAuthorityCore(testCtx, "https://accounts.example.test", "page-by-name", key), "the host starts")
	_, err = svc.AuthorityCore()
	require.NoError(t, err, "and serves")
	require.NoError(t, svc.KeepAuthorityCoreCurrent(testCtx), "a delivery pass reports it and goes on")

	condition, flagged := needsReview(t, admin, org, installation)
	require.True(t, flagged, "the installation read says it needs its review")
	require.Equal(t, alias, condition.GetSubject())
	require.Contains(t, condition.GetDetail(), "An organization administrator clears it: uninstall it, then install it through its review.")

	require.NoError(t, testService.UninstallSolution(testCtx, admin, org, installation))
	_, flagged = needsReview(t, admin, org, installation)
	require.False(t, flagged)
}

// A delivery that would make a module role-gated while it is installed by name
// is refused before anything is recorded, as a delivery conflict naming each
// installation; once uninstalled, nothing of that refusal stands.
func TestDeliveryRefusesToMakeAModuleInstalledByNameRoleGated(t *testing.T) {
	clearData(t)
	signer := wireSignedDelivery(t)
	admin, org := mustUserAndOrg(t, testCtx, "admin@gating.test", "gating", "Gating")
	installation, _, alias := installForExposure(t, admin, org)

	m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: alias, Alias: alias, Kind: solutionhost.KindModule, Generation: 2,
		Host: solutionhost.HostTarget{Coordinate: legacyDeliveryCoordinate, Domain: "acme"}, Release: solutionhost.Release{Publisher: "acme", Name: alias, Version: "2.0.0"},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "app"}}, Authority: &solutionhost.Authority{Principal: alias, ResourceKinds: []string{alias + ".note"}},
		Frontend: &solutionhost.Frontend{Title: "Page", ExposedModule: "./Page", HostContract: 1}}
	raw, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	note := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: alias + ".note", Actions: actions}}
	}
	doc, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: alias, Generation: 2, CoreManifestDigest: delivery.Digest(raw),
		Roles: []*compositionv1.RoleV1{{Name: business.InstallerRole, Scopes: note("read", "write")}, {Name: "viewer", Scopes: note("read")}}})
	require.NoError(t, err)
	envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
	require.NoError(t, err)
	req := &compositionv1.ApplyManifestRequest{Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())},
		Credential: "delivery:" + alias, Authority: &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}}

	_, err = testService.ApplyComposition(testCtx, req)
	require.ErrorIs(t, err, business.ErrSolutionDeliveryConflict)
	require.ErrorContains(t, err, "installation "+installation+" at organization "+org)
	require.ErrorContains(t, err, "declares roles while it is installed by name")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		recorded, err := testStore.ReadDeliveredDocument(ctx, alias, 2)
		require.Nil(t, recorded, "the refused generation is not recorded")
		return err
	}))
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		held, err := tx.ModuleNamed(alias)
		require.Equal(t, uint64(1), held.Generation, "the core composes nothing of it")
		return err
	}))

	require.NoError(t, testService.UninstallSolution(testCtx, admin, org, installation))
	if _, err := testService.ApplyComposition(testCtx, req); err != nil {
		require.NotContains(t, err.Error(), "installed by name", "no installation by name stands")
	}
}
