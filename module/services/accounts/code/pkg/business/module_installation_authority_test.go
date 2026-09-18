package business_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

type authorityInstallationStore struct {
	business.Store
	result        business.ModuleInstallationResult
	failure       error
	calls         int
	commitFailure error
}

func (s *authorityInstallationStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *authorityInstallationStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return s.commitFailure
}
func (s *authorityInstallationStore) GetOrganizationBySlug(context.Context, string) (*gen.Organization, error) {
	return &gen.Organization{Id: s.result.OrganizationID}, nil
}
func (s *authorityInstallationStore) ReconcileModuleInstallation(context.Context, *business.InstallSolutionParams, []string, bool) (*business.ModuleInstallationResult, error) {
	s.calls++
	result := s.result
	return &result, s.failure
}
func authorityInstallationFixture(t *testing.T, module string) (*business.Service, *authorityInstallationStore, business.ModuleCaller, *business.InstallerPolicy, business.ModuleInstallationRequest) {
	t.Helper()
	d := business.InstallerDelegation{
		Prefix: "example-installer", ModuleID: "acme.example/" + module,
		OrganizationID:     "11111111-1111-4111-8111-111111111111",
		OwnerPrincipalID:   "22222222-2222-4222-8222-222222222222",
		RoleID:             "33333333-3333-4333-8333-333333333333",
		SolutionIdentifier: "example-solution", AgentIdentifiers: []string{"acme.example/" + module + ":1.0.0"},
		RolePermissions:  []string{"example:read", "example:write"},
		AllowedAudiences: []string{"example.api", "example.worker"}, AllowedScopes: []string{"example", "example-subset"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID(d.Prefix), BoundOrg: d.OrganizationID}
	req := business.ModuleInstallationRequest{
		AuthorityReferenceVersion: business.ModuleInstallationAuthorityVersion,
		ModuleID:                  d.ModuleID, OrganizationSlug: "example-org", AgentIdentifier: d.AgentIdentifiers[0], SolutionIdentifier: d.SolutionIdentifier,
		RoleID: d.RoleID, ExpectedRolePermissions: slices.Clone(d.RolePermissions), AllowedAudiences: slices.Clone(d.AllowedAudiences), AllowedScopes: slices.Clone(d.AllowedScopes),
		DisplayName: "Example", RootScopeLabel: "Example",
	}
	store := &authorityInstallationStore{result: business.ModuleInstallationResult{
		State: "ready", OrganizationID: d.OrganizationID, PrincipalID: "44444444-4444-4444-8444-444444444444",
		InstallationID: "55555555-5555-4555-8555-555555555555", ScopeNodeID: "66666666-6666-4666-8666-666666666666", GrantID: "77777777-7777-4777-8777-777777777777",
	}}
	service, err := business.NewService(store)
	require.NoError(t, err)
	return service, store, caller, &business.InstallerPolicy{Version: "accounts.module-installation-policy/v1", Delegations: []business.InstallerDelegation{d}}, req
}
func verifiedAuthority(t *testing.T, svc *business.Service, caller business.ModuleCaller, policy *business.InstallerPolicy, req business.ModuleInstallationRequest) *business.ModuleInstallationAuthorityReference {
	t.Helper()
	result, err := svc.ReconcileModuleInstallation(context.Background(), caller, policy, req, false)
	require.NoError(t, err)
	require.NotNil(t, result.AuthorityReference)
	require.Equal(t, business.ModuleInstallationAuthorityVersion, result.AuthorityReference.SchemaVersion)
	require.Regexp(t, `^sha256:[a-f0-9]{64}$`, result.AuthorityReference.Digest)
	return result.AuthorityReference
}
func TestInstallationAuthorityIsGenericAndStableForSameVerifiedContract(t *testing.T) {
	var digests []string
	for _, module := range []string{"example-one", "example-two"} {
		t.Run(module, func(t *testing.T) {
			svc, store, caller, policy, req := authorityInstallationFixture(t, module)
			first := verifiedAuthority(t, svc, caller, policy, req)
			digests = append(digests, first.Digest)
			// This controlled store tests fingerprint exclusions, not permission to
			// mutate immutable display names in the real installation store. Labels,
			// delegation renewal, request mode and ordering are not authority identity.
			// Software/images are not fields of the Accounts request at all.
			req.DisplayName, req.RootScopeLabel = "New label", "New root label"
			policy.Delegations[0].ExpiresAt = time.Now().Add(2 * time.Hour)
			slices.Reverse(policy.Delegations[0].RolePermissions)
			slices.Reverse(policy.Delegations[0].AllowedAudiences)
			slices.Reverse(policy.Delegations[0].AllowedScopes)
			require.Equal(t, first, verifiedAuthority(t, svc, caller, policy, req))
			applied, err := svc.ReconcileModuleInstallation(context.Background(), caller, policy, req, true)
			require.NoError(t, err)
			require.False(t, applied.Changed)
			require.Equal(t, first, applied.AuthorityReference)
			require.Equal(t, 3, store.calls)
		})
	}
	require.NotEqual(t, digests[0], digests[1], "one module's contract must not satisfy another")
}
func TestInstallationAuthorityChangesForEveryAuthorityDimension(t *testing.T) {
	for _, field := range []string{"module", "organization", "agent", "solution", "role", "permissions", "audiences", "scopes", "owner", "installer", "principal", "installation", "scope-node", "grant"} {
		t.Run(field, func(t *testing.T) {
			svc, store, caller, policy, req := authorityInstallationFixture(t, "example-one")
			before := verifiedAuthority(t, svc, caller, policy, req)
			d := &policy.Delegations[0]
			id := "88888888-8888-4888-8888-888888888888"
			switch field {
			case "module":
				req.ModuleID = "acme.example/other"
				d.ModuleID = req.ModuleID
				req.AgentIdentifier = req.ModuleID + ":1.0.0"
				d.AgentIdentifiers = []string{req.AgentIdentifier}
			case "organization":
				d.OrganizationID = id
				caller.BoundOrg = id
				store.result.OrganizationID = id
			case "agent":
				req.AgentIdentifier = req.ModuleID + ":2.0.0"
				d.AgentIdentifiers = []string{req.AgentIdentifier}
			case "solution":
				req.SolutionIdentifier = "another-solution"
				d.SolutionIdentifier = req.SolutionIdentifier
			case "role":
				req.RoleID = id
				d.RoleID = id
			case "permissions":
				d.RolePermissions = []string{"example:read"}
				req.ExpectedRolePermissions = slices.Clone(d.RolePermissions)
			case "audiences":
				d.AllowedAudiences = []string{"example.api"}
				req.AllowedAudiences = slices.Clone(d.AllowedAudiences)
			case "scopes":
				d.AllowedScopes = []string{"example"}
				req.AllowedScopes = slices.Clone(d.AllowedScopes)
			case "owner":
				d.OwnerPrincipalID = id
			case "installer":
				d.Prefix = "another-installer"
				caller.PrincipalID = business.ModulePrincipalID(d.Prefix)
			case "principal":
				store.result.PrincipalID = id
			case "installation":
				store.result.InstallationID = id
			case "scope-node":
				store.result.ScopeNodeID = id
			case "grant":
				store.result.GrantID = id
			}
			// The controlled store models a separately approved, healthy transition;
			// it does not prove that the real database permits that transition.
			require.NotEqual(t, before.Digest, verifiedAuthority(t, svc, caller, policy, req).Digest)
		})
	}
}
func TestInstallationAuthorityCannotBypassPolicyOrPersistence(t *testing.T) {
	for _, fault := range []string{"expired", "revoked", "wrong-org", "wrong-role", "wrong-agent", "wrong-owner-eligibility", "role-drift", "store-unavailable", "missing-id", "nil-id", "unknown-version"} {
		t.Run(fault, func(t *testing.T) {
			svc, store, caller, policy, req := authorityInstallationFixture(t, "example-one")
			switch fault {
			case "expired":
				policy.Delegations[0].ExpiresAt = time.Now().Add(-time.Hour)
			case "revoked":
				policy.Delegations = nil
			case "wrong-org":
				caller.BoundOrg = "88888888-8888-4888-8888-888888888888"
			case "wrong-role":
				req.RoleID = "88888888-8888-4888-8888-888888888888"
			case "wrong-agent":
				req.AgentIdentifier = req.ModuleID + ":2.0.0"
			case "wrong-owner-eligibility", "role-drift", "store-unavailable":
				store.failure = errors.New("controlled store rejection")
			case "missing-id":
				store.result.GrantID = ""
			case "nil-id":
				store.result.PrincipalID = "00000000-0000-0000-0000-000000000000"
			case "unknown-version":
				req.AuthorityReferenceVersion = "future"
			}
			result, err := svc.ReconcileModuleInstallation(context.Background(), caller, policy, req, false)
			require.Error(t, err)
			if result != nil {
				require.Nil(t, result.AuthorityReference)
			}
		})
	}
}
func TestInstallationAuthorityIsOptInAndAbsentHasNoReference(t *testing.T) {
	svc, store, caller, policy, req := authorityInstallationFixture(t, "example-one")
	req.AuthorityReferenceVersion = ""
	result, err := svc.ReconcileModuleInstallation(context.Background(), caller, policy, req, false)
	require.NoError(t, err)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "authorityReference")
	raw, err = json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "authorityReferenceVersion")
	req.AuthorityReferenceVersion = business.ModuleInstallationAuthorityVersion
	store.result = business.ModuleInstallationResult{State: "absent", OrganizationID: caller.BoundOrg}
	result, err = svc.ReconcileModuleInstallation(context.Background(), caller, policy, req, false)
	require.NoError(t, err)
	require.Nil(t, result.AuthorityReference)
}
func TestInstallationAuthorityCanonicalGolden(t *testing.T) {
	svc, _, caller, policy, req := authorityInstallationFixture(t, "example-one")
	got := verifiedAuthority(t, svc, caller, policy, req)
	// Pinned independently from the documented canonical JSON, not from the
	// production encoder. Changing the contract requires a version decision.
	require.Equal(t, "sha256:55816f0b3346273e460d1745e4c46f6c74a63a0baf4ed755fe3dcc394f74e570", got.Digest)
	require.False(t, strings.Contains(got.Digest, req.AgentIdentifier))
}

func TestInstallationAuthorityIsWithheldWhenTransactionCommitFails(t *testing.T) {
	svc, store, caller, policy, req := authorityInstallationFixture(t, "example-one")
	store.commitFailure = errors.New("commit outcome unknown")
	result, err := svc.ReconcileModuleInstallation(context.Background(), caller, policy, req, true)
	require.ErrorIs(t, err, store.commitFailure)
	require.Nil(t, result)
	require.Equal(t, 1, store.calls)
}

func TestInstallationAuthorityIgnoresOrganizationLookupCase(t *testing.T) {
	svc, _, caller, policy, req := authorityInstallationFixture(t, "example-one")
	before := verifiedAuthority(t, svc, caller, policy, req)
	req.OrganizationSlug = strings.ToUpper(req.OrganizationSlug)
	require.Equal(t, before, verifiedAuthority(t, svc, caller, policy, req))
}
