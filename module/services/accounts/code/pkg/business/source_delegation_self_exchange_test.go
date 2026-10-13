package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The exchange when the DELEGATOR IS THE CALLER (issue #1121, m1).
//
// This is the shape unattended work runs in, and until now it was reached only
// through fixtures that happen to declare a self-audience — where a successful
// parse says nothing about why the audience link held. The link is the whole
// reason a reference is safe to hold: a delegation is a grant to one binding of
// one module, that binding declares the audience it may call, and only the
// module named as that audience may present the reference. For a module that
// executes a declared release the audience is itself — it mints the capability
// and then admits it as its own incoming execution context — so delegator and
// caller are one principal and the comparison is `prefix == prefix`.
//
// Asserted here directly, on the exchange, with the two neighbouring states
// that must still refuse: an audience naming somebody else, and a caller
// naming a binding of its own that the delegation does not authorize.
func TestADelegatorExchangesItsOwnReferenceWhenItIsTheAudience(t *testing.T) {
	exchange := func(t *testing.T, audience string) (SourceOperationContextAuthority, error) {
		t.Helper()
		read := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read"}}}
		write := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"write"}}}
		both := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
		// One binding, declaring BOTH grants: the delegation scopes a person's
		// connect records against, and the audience the executing module admits
		// its own occurrences under. That pairing is what selfAdmittingBinding
		// allows, and it is why a composition can run unattended work at all.
		occurrences := ModuleOperationAudience{
			Audience: audience, InvokeScopes: both, LookupScopes: read, SourceDelegationScopes: write,
		}
		runtime := ModulePrincipalGrant{
			Prefix: "runtime", Tenant: testEnvelopeTenant,
			OperationAudiences: map[string]ModuleOperationAudience{"occurrences": occurrences},
		}
		store := &referenceExchangeStore{delegation: &SourceDelegation{
			ID: "example-delegation", OrgID: testEnvelopeTenant, PrincipalID: "example-person",
			SourceID: "example-source", ModulePrefix: "runtime", BindingID: "occurrences",
			BindingDigest: SourceDelegationBindingDigest(occurrences),
		}}
		s := &Service{store: store}
		s.SetModuleAuthorityReads(reviewLiveAuthority{service: s}, nil)
		s.SetModuleEnvelopeStore(&fakeEnvelopeStore{latest: 1})
		s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID("runtime"): runtime}, 1, nil)
		caller := ModuleCaller{PrincipalID: ModulePrincipalID("runtime"), BoundOrg: testEnvelopeTenant}
		return s.AuthorizeDelegationReferenceExchange(context.Background(), caller, store.delegation.ID, "occurrences", false)
	}

	authority, err := exchange(t, "runtime")
	require.NoError(t, err, "the module that executes the release is the audience its own delegation names")
	// The capability is the person's, used by the module: exactly what the
	// parent-token arm produces, so no consumer can tell which arm admitted it.
	require.Equal(t, "example-person", authority.OwnerPrincipalID)
	require.Equal(t, ModulePrincipalID("runtime"), authority.PrincipalID)
	require.Equal(t, "runtime", authority.ModulePrefix)
	require.Equal(t, "runtime", authority.Audience)
	require.Equal(t, testEnvelopeTenant, authority.Tenant, "the tenant is the delegation's organization, never the caller's request")
	require.Equal(t, "example-delegation", authority.Delegation.ID)
	// What the person delegated, and what this one call may do with it: the
	// binding's delegation scopes, and the caller's invoke reach narrowed to
	// them. Write and not read: a self-exchange is not a wider grant.
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"write"}}}, authority.Scopes)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"write"}}}, authority.ActorScopes)

	// The same registry with the audience naming somebody else: the caller is
	// no longer the audience of the delegation it holds, and holding the
	// delegation is not what authorizes presenting it.
	_, err = exchange(t, "somebody-else")
	require.ErrorIs(t, err, ErrSourceDelegationInvalid,
		"a module that is not the audience cannot exchange its own delegation")
}

// A caller that is the audience but names a binding of its own the delegation
// does not authorize is refused: the binding half of the link is checked
// separately from the audience half.
func TestASelfExchangeStillNeedsTheBindingItNames(t *testing.T) {
	read := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read"}}}
	write := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"write"}}}
	both := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
	occurrences := ModuleOperationAudience{
		Audience: "runtime", InvokeScopes: both, LookupScopes: read, SourceDelegationScopes: write,
	}
	runtime := ModulePrincipalGrant{
		Prefix: "runtime", Tenant: testEnvelopeTenant,
		OperationAudiences: map[string]ModuleOperationAudience{"occurrences": occurrences},
	}
	store := &referenceExchangeStore{delegation: &SourceDelegation{
		ID: "example-delegation", OrgID: testEnvelopeTenant, PrincipalID: "example-person",
		SourceID: "example-source", ModulePrefix: "runtime", BindingID: "occurrences",
		BindingDigest: SourceDelegationBindingDigest(occurrences),
	}}
	s := &Service{store: store}
	s.SetModuleAuthorityReads(reviewLiveAuthority{service: s}, nil)
	s.SetModuleEnvelopeStore(&fakeEnvelopeStore{latest: 1})
	s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID("runtime"): runtime}, 1, nil)
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("runtime"), BoundOrg: testEnvelopeTenant}

	_, err := s.AuthorizeDelegationReferenceExchange(context.Background(), caller, store.delegation.ID, "not-declared", false)
	require.ErrorIs(t, err, ErrSourceDelegationInvalid, "a binding the module does not declare authorizes nothing")
}
