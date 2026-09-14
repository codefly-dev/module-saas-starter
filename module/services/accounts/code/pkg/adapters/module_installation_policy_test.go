package adapters

import (
	"accounts/pkg/business"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func installerPolicyFixture() (*business.InstallerPolicy, business.ModuleCaller, business.ModuleInstallationRequest) {
	d := business.InstallerDelegation{Prefix: "example-installer", OrganizationID: "11111111-1111-4111-8111-111111111111", ModuleID: "acme.example/solution", AgentIdentifiers: []string{"acme.example/solution:1.0.0"}, SolutionIdentifier: "example-solution", RoleID: "22222222-2222-4222-8222-222222222222", RolePermissions: []string{"documents:read"}, AllowedAudiences: []string{"example.api"}, AllowedScopes: []string{"documents"}, OwnerPrincipalID: "33333333-3333-4333-8333-333333333333", ExpiresAt: time.Now().Add(time.Hour)}
	return &business.InstallerPolicy{Version: "accounts.module-installation-policy/v1", Delegations: []business.InstallerDelegation{d}}, business.ModuleCaller{PrincipalID: business.ModulePrincipalID(d.Prefix), BoundOrg: d.OrganizationID}, business.ModuleInstallationRequest{ModuleID: d.ModuleID, OrganizationSlug: "example-org", AgentIdentifier: d.AgentIdentifiers[0], SolutionIdentifier: d.SolutionIdentifier, RoleID: d.RoleID, ExpectedRolePermissions: d.RolePermissions, AllowedAudiences: d.AllowedAudiences, AllowedScopes: d.AllowedScopes}
}
func TestInstallerDelegationScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*business.InstallerPolicy, *business.ModuleCaller, *business.ModuleInstallationRequest)
	}{
		{"wrong caller", func(_ *business.InstallerPolicy, c *business.ModuleCaller, _ *business.ModuleInstallationRequest) {
			c.PrincipalID = business.ModulePrincipalID("another")
		}},
		{"cross organization", func(_ *business.InstallerPolicy, c *business.ModuleCaller, _ *business.ModuleInstallationRequest) {
			c.BoundOrg = "44444444-4444-4444-8444-444444444444"
		}},
		{"wrong module", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.ModuleID = "acme.example/other"
		}},
		{"version expansion", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.AgentIdentifier = "acme.example/solution:2.0.0"
		}},
		{"solution expansion", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.SolutionIdentifier = "another"
		}},
		{"role expansion", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.RoleID = "another"
		}},
		{"scope expansion", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.AllowedScopes = []string{"*"}
		}},
		{"unbounded ceiling", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.AllowedAudiences = nil
		}},
		{"permission expansion", func(_ *business.InstallerPolicy, _ *business.ModuleCaller, r *business.ModuleInstallationRequest) {
			r.ExpectedRolePermissions = []string{"documents:write"}
		}},
		{"expiry", func(p *business.InstallerPolicy, _ *business.ModuleCaller, _ *business.ModuleInstallationRequest) {
			p.Delegations[0].ExpiresAt = time.Now().Add(-time.Second)
		}},
		{"revoked", func(p *business.InstallerPolicy, _ *business.ModuleCaller, _ *business.ModuleInstallationRequest) {
			p.Delegations = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, c, r := installerPolicyFixture()
			_, err := p.Authorize(c, r, time.Now())
			require.NoError(t, err)
			tc.mutate(p, &c, &r)
			_, err = p.Authorize(c, r, time.Now())
			require.ErrorIs(t, err, business.ErrInstallerDenied)
		})
	}
}
func TestInstallerPolicyRejectsUnknownAndUnboundedDeclarations(t *testing.T) {
	p, _, _ := installerPolicyFixture()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	_, err = business.ParseInstallerPolicy(strings.NewReader(string(raw)))
	require.NoError(t, err)
	for _, raw := range []string{strings.Replace(string(raw), `"version":`, `"unknown":true,"version":`, 1), string(raw) + " {}", strings.Replace(string(raw), `"documents"`, `"*"`, 1), strings.Replace(string(raw), `"example.api"`, `""`, 1)} {
		_, err = business.ParseInstallerPolicy(strings.NewReader(raw))
		require.Error(t, err)
	}
}

func TestInstallerPolicyRejectsOversizedProjection(t *testing.T) {
	p, _, _ := installerPolicyFixture()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	const maxPolicyBytes = 1 << 20
	boundary := string(raw) + strings.Repeat(" ", maxPolicyBytes-len(raw))
	_, err = business.ParseInstallerPolicy(strings.NewReader(boundary))
	require.NoError(t, err)
	for _, suffix := range []string{" ", "{}", "invalid"} {
		t.Run(suffix, func(t *testing.T) {
			_, err := business.ParseInstallerPolicy(strings.NewReader(boundary + suffix))
			require.Error(t, err)
		})
	}
}
