//go:build !pure

package infra_test

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExecutableArtifactPostgresConsentRevocationAndTenantFloor(t *testing.T) {
	org, owner, role, installation, _ := installFixture(t, "definitions", "configure")
	// Sources names the BINDING the consented target records, not the route
	// alias: an alias is reusable, so keying on one would let a replacement
	// binding inherit this installation's approvals. Read it off the target the
	// installation names rather than restating it, so the fixture and the policy
	// cannot drift apart.
	// On the control plane, like the service path: solution_targets is a
	// global relation with exact grants, so a bare read is permission-denied
	// (SQLSTATE 42501) — the binding the policy installs is only readable there.
	var consented *business.SolutionTarget
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var e error
		consented, e = testStore.GetSolutionTarget(ctx, installation.TargetId)
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, consented, "the fixture's installation must name a live target")
	policy := business.ExecutableArtifactPolicy{Schema: "example.artifact/v1", Sources: map[string]string{"acme.example.solution": consented.BindingID}, Activate: business.ArtifactPermission{Resource: "definitions", Action: "configure"}, Run: business.ArtifactPermission{Resource: "definitions", Action: "configure"}, Contracts: []business.ArtifactContract{{Kind: "model", Name: "example/profile", Digest: "sha256:" + strings.Repeat("a", 64)}}, RequiredKinds: []string{"model"}}
	caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID("example"), BoundOrg: org}
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{caller.PrincipalID: {Prefix: "example", Tenant: org, ArtifactPolicies: map[string]business.ExecutableArtifactPolicy{"content": policy}}})
	request := business.ExecutableArtifactRequest{Installation: installation.Id, Policy: "content", Identity: business.ExecutableArtifactIdentity{Schema: policy.Schema, Source: "acme.example.solution", Subject: []byte(`{"version":9007199254740993,"dependencies":[]}`), Contracts: policy.Contracts, ExpectedRevision: 9007199254740993}}
	decide := func(action string) (*business.ExecutableArtifactApproval, error) {
		return svc.DecideExecutableArtifact(auth.WithVerifiedDatabaseIdentity(testCtx, owner, org), caller, org, owner, "example", action, request)
	}
	_, err = decide("authorize")
	require.ErrorContains(t, err, "exact artifact approval")
	// Concurrent explicit activation retries and a lost response leave one exact
	// immutable receipt, not multiple approval generations or a moved pointer.
	const count = 8
	var wg sync.WaitGroup
	results := make(chan *business.ExecutableArtifactApproval, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a, e := decide("approve"); results <- a; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var first *business.ExecutableArtifactApproval
	for a := range results {
		if first == nil {
			first = a
		}
		require.Equal(t, first.ID, a.ID)
		require.Equal(t, first.Subject, a.Subject)
	}
	_, err = decide("authorize")
	require.NoError(t, err)
	// A different member runs a tenant-approved graph using an exact resource
	// grant. Reading the approver through that member's private identity would
	// fail in production; approval is tenant consent, not an impersonated read.
	runner := seedUser(t)
	seedHumanPrincipal(t, runner, "Example Runner")
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, runner, "member"))
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.AssignRole(ctx, &gen.RoleAssignment{Id: uuid.NewString(), SubjectId: runner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: role, OrgId: org, Scope: installation.Id})
	}))
	_, err = svc.DecideExecutableArtifact(auth.WithVerifiedDatabaseIdentity(testCtx, runner, org), caller, org, runner, "example", "authorize", request)
	require.NoError(t, err)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, e := storetx.Tx(ctx).Exec(ctx, `UPDATE users SET status='suspended' WHERE uuid=$1`, runner)
		return e
	}))
	_, err = svc.DecideExecutableArtifact(auth.WithVerifiedDatabaseIdentity(testCtx, runner, org), caller, org, runner, "example", "authorize", request)
	require.ErrorContains(t, err, "current artifact authority")
	// New content with an unchanged symbolic source cannot inherit prior consent.
	original := append([]byte{}, request.Identity.Subject...)
	request.Identity.Subject = []byte(`{"version":9007199254740994,"dependencies":[]}`)
	_, err = decide("authorize")
	require.ErrorContains(t, err, "exact artifact approval")
	request.Identity.Subject = original
	foreignOrg := seedOrg(t, owner)
	require.NoError(t, testStore.WithOrgTx(testCtx, foreignOrg, func(ctx context.Context) error {
		got, e := testStore.GetExecutableArtifactApproval(ctx, org, installation.Id, caller.PrincipalID, first.Digest)
		require.NoError(t, e)
		require.Nil(t, got, "wrong-tenant parameter must not defeat RLS")
		return nil
	}))
	require.Error(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		_, e := storetx.Tx(ctx).Exec(ctx, `UPDATE executable_artifact_approvals SET subject='forged' WHERE id=$1`, first.ID)
		require.Error(t, e, "application role cannot rewrite approved content")
		return e
	}))
	// A removed policy and unhealthy installation must not trap old consent.
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{caller.PrincipalID: {Prefix: "example", Tenant: org}})
	_, _, err = svc.RevokeApprovedExecutableArtifact(auth.WithVerifiedDatabaseIdentity(testCtx, owner, org), caller, org, owner, "example", installation.Id, first.ID, func(business.ArtifactPermission) error { return nil })
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{caller.PrincipalID: {Prefix: "example", Tenant: org, ArtifactPolicies: map[string]business.ExecutableArtifactPolicy{"content": policy}}})
	require.NoError(t, err)
	_, err = decide("authorize")
	require.ErrorContains(t, err, "revoked")
	_, err = decide("approve")
	require.ErrorContains(t, err, "revoked", "lost reply replay cannot revive revoked approval")
	require.Error(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		_, e := storetx.Tx(ctx).Exec(ctx, `UPDATE executable_artifact_approvals SET revoked_at=NULL,revoked_by=NULL WHERE id=$1`, first.ID)
		return e
	}))
	// Verify pool reuse clears tenant state, including after a rolled-back write.
	require.NoError(t, testStore.WithOrgTx(testCtx, foreignOrg, func(ctx context.Context) error {
		var count int
		e := storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM executable_artifact_approvals WHERE id=$1`, first.ID).Scan(&count)
		require.Zero(t, count)
		return e
	}))

}
