package business

import (
	"testing"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"

	solutionhost "github.com/codefly-dev/authority/libraries/solutionhost/go"
	"github.com/stretchr/testify/require"
)

func TestDeliveredRolesCannotExceedManifestAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, kind, action string
		operations         map[string][]string
		ceiled, allowed    bool
	}{
		{name: "served kind", kind: "pages", action: "write", allowed: true},
		{name: "served ceiling", kind: "pages", action: "read", ceiled: true, allowed: true},
		{name: "served widening", kind: "pages", action: "write", ceiled: true},
		{name: "unknown kind", kind: "other", action: "read"},
		{name: "invoke", kind: "documents", action: "write", operations: map[string][]string{"invoke": {"documents:write"}}, allowed: true},
		{name: "lookup", kind: "documents", action: "read", operations: map[string][]string{"lookup": {"documents:read"}}, allowed: true},
		{name: "headless", kind: "documents", action: "write", operations: map[string][]string{"headless": {"documents:write"}}},
		{name: "lookup cannot borrow headless", kind: "documents", action: "write", operations: map[string][]string{"lookup": {"documents:read"}, "headless": {"documents:write"}}},
		{name: "invoke widening", kind: "documents", action: "write", operations: map[string][]string{"invoke": {"documents:read"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &solutionhost.ModuleManifest{Alias: "example", Kind: solutionhost.KindSolution, Authority: &solutionhost.Authority{
				ResourceKinds: []string{"pages"}, Bindings: []solutionhost.AuthorityBinding{{ID: "documents", Audience: "documents", Operations: tc.operations}},
			}}
			doc := &compositionv1.AuthorityManifestV1{Roles: []*compositionv1.RoleV1{{Name: "user", Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: tc.kind, Actions: []string{tc.action}, ResourceIds: []string{"one"}}}}}}
			if tc.ceiled {
				doc.ScopeCeilings = []*authorityv1.WorkScopeV1{{ResourceKind: "pages", Actions: []string{"read"}}}
			}
			err := validateAuthorityRoles(manifest, doc)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
				require.ErrorContains(t, err, `role "user"`)
			}
		})
	}
}

func TestDeliveredRoleAddressedToBindingAudience(t *testing.T) {
	manifest := &solutionhost.ModuleManifest{
		Alias: "builder", Kind: solutionhost.KindSolution, Generation: 1,
		Authority: &solutionhost.Authority{Principal: "builder", ResourceKinds: []string{"pages"}, Bindings: []solutionhost.AuthorityBinding{
			{ID: "tools", Audience: "robin", Operations: map[string][]string{"invoke": {"tools:read"}}},
			{ID: "other", Audience: "other", Operations: map[string][]string{"invoke": {"tools:write"}}},
		}},
	}
	for _, tc := range []struct {
		name, audience, action string
		accepted               bool
	}{
		{"addressed only", "robin", "read", true},
		{"another audience's action", "robin", "write", false},
		{"undeclared audience", "missing", "read", false},
		{"binding id is not its audience", "tools", "read", false},
		{"empty audience", "", "read", false},
		{"wildcard action", "robin", "*", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &compositionv1.RoleAudienceV1{Audience: tc.audience, Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "tools", Actions: []string{tc.action}}}}
			doc := &compositionv1.AuthorityManifestV1{Roles: []*compositionv1.RoleV1{{Name: "admin", Audiences: []*compositionv1.RoleAudienceV1{entry}}}}
			err := validateAuthorityRoles(manifest, doc)
			if tc.accepted {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
			}
			// A valid direct scope cannot mask an invalid addressed grant.
			doc.Roles[0].Scopes = []*authorityv1.WorkScopeV1{{ResourceKind: "pages", Actions: []string{"read"}}}
			err = validateAuthorityRoles(manifest, doc)
			if tc.accepted {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
			}
		})
	}
}
