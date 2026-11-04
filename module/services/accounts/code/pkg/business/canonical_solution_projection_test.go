package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
)

func TestCanonicalSolutionEntitlementRequiresAcknowledgedHeldInstallation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*authority.HeldInstallation, *domain.Module, *AvailableSolutionTarget, *string)
	}{
		{"pending setup", func(h *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			h.Installation.Status = domain.Pending
		}},
		{"revoked", func(h *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			h.Installation.Status = domain.Revoked
		}},
		{"owner without a person grant", func(h *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			h.Roles = nil
		}},
		{"another organization", func(h *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			h.Installation.Organization = "other"
		}},
		{"another target", func(h *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			h.Installation.Target = "other"
		}},
		{"alias mistaken for principal", func(_ *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, p *string) {
			*p = "example"
		}},
		{"replacement binding principal", func(_ *authority.HeldInstallation, _ *domain.Module, _ *AvailableSolutionTarget, p *string) {
			*p = "replacement-principal"
		}},
		{"stale core delivery", func(_ *authority.HeldInstallation, m *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			m.Generation--
		}},
		{"missing target identity", func(_ *authority.HeldInstallation, _ *domain.Module, target *AvailableSolutionTarget, _ *string) {
			target.TargetID = ""
		}},
		{"missing binding", func(_ *authority.HeldInstallation, _ *domain.Module, target *AvailableSolutionTarget, _ *string) {
			target.BindingID = ""
		}},
		{"module uses its own path", func(_ *authority.HeldInstallation, _ *domain.Module, target *AvailableSolutionTarget, _ *string) {
			target.Kind = "module"
		}},
		{"module without roles", func(_ *authority.HeldInstallation, m *domain.Module, target *AvailableSolutionTarget, _ *string) {
			m.Kind, m.Roles, target.Kind = domain.KindModule, nil, "module"
		}},
		{"module page target read as a solution", func(_ *authority.HeldInstallation, m *domain.Module, _ *AvailableSolutionTarget, _ *string) {
			m.Kind = domain.KindModule
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, module, target, principal := canonicalProjectionFixture()
			tc.change(&held, &module, &target, &principal)
			if got := canonicalSolutionEntitlement("org", held, module, &target, principal); got != nil {
				t.Fatalf("unqualified installation entered gateway entitlements: %v", got)
			}
		})
	}
}

func TestCanonicalSolutionEntitlementPreservesHostTargetAndCoreIdentity(t *testing.T) {
	held, module, target, principal := canonicalProjectionFixture()
	got := canonicalSolutionEntitlement("org", held, module, &target, principal)
	if got == nil || got.TargetId != target.TargetID || got.InstallationId != held.Installation.ID || !got.Healthy {
		t.Fatalf("acknowledged held installation was not projected: %v", got)
	}
	if got.RootScopeNodeId != "" {
		t.Fatal("canonical solution invented a legacy RBAC root")
	}
	if canonicalSolutionEntitlement("org", held, module, nil, principal) != nil {
		t.Fatal("withdrawn target remained entitled")
	}
}

// A module page that declares roles is projected as a solution is.
func TestCanonicalSolutionEntitlementProjectsAModulePageDeclaringRoles(t *testing.T) {
	held, module, target, principal := canonicalProjectionFixture()
	module.Kind, target.Kind = domain.KindModule, "module"
	got := canonicalSolutionEntitlement("org", held, module, &target, principal)
	if got == nil || got.TargetId != target.TargetID || got.InstallationId != held.Installation.ID {
		t.Fatalf("a module page's held role was not projected: %v", got)
	}
}

func canonicalProjectionFixture() (authority.HeldInstallation, domain.Module, AvailableSolutionTarget, string) {
	return authority.HeldInstallation{Installation: domain.Installation{
		ID: "canonical-installation", Organization: "org", Target: "example", Status: domain.Active,
		AgentRole: "machine", InstalledBy: "owner",
	}, Roles: []string{"reader"}}, domain.Module{
		Name: "example", Principal: "signed-principal", Kind: domain.KindSolution, Generation: 7,
		Roles: map[string][]*basev0.WorkScopeV1{"reader": {{ResourceKind: "example.page", Actions: []string{"read"}}}},
	}, AvailableSolutionTarget{
		Kind: "solution", TargetID: "immutable-host-target", BindingID: "delivered-binding", RouteAlias: "example", AppliedGeneration: 7,
	}, "signed-principal"
}
