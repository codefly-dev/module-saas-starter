package business_test

import (
	"testing"

	"accounts/pkg/business"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
)

// These comparisons do not establish transaction/lock behavior. The SQL
// activation fixture must additionally exercise the store's locked read.
func TestReviewedSolutionPlanRequiresExactCanonicalReceiptAndPrerequisites(t *testing.T) {
	module := business.ModuleInstallationPrerequisite{
		InstallationID: "module-installation", Organization: "org", Revision: 4,
		TargetID: "host-module-target", BindingID: "module-binding", PrincipalID: "module-principal",
		Generation: 7, DeliveryDigest: "delivery-digest", ScopeDigest: "consent-scope-digest", RoleID: "scoped-role",
	}
	receipt := domain.ConsentReceipt{OperationID: "operation", Intent: "original-intent", Revision: 3}
	plan := business.SolutionInstallationPlan{
		InstallationID: "solution-installation", Organization: "org", Target: "example", Revision: 3, Receipt: receipt,
		MachineRole: "machine", SetupJobID: "acknowledged-job",
		Approval:     domain.ConsentApproval{TargetID: "solution-principal", Generation: 2, Digest: "offer-digest", OperationID: "operation"},
		HostTargetID: "host-solution-target", BindingID: "solution-binding", Release: "acme.example/example@1.0.0", DeliveryDigest: "solution-delivery",
		Dependencies: []business.ModuleInstallationPrerequisite{module},
	}
	inst := domain.Installation{ID: plan.InstallationID, Organization: plan.Organization, Target: plan.Target, AgentRole: plan.MachineRole, Revision: plan.Revision, Consent: receipt, Status: domain.Active}
	if err := plan.ValidateActivation(inst, []business.ModuleInstallationPrerequisite{module}, plan.SetupJobID, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*domain.Installation, *[]business.ModuleInstallationPrerequisite)
	}{
		{"revoke after setup", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) { *rows = nil }},
		{"module revision changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) { (*rows)[0].Revision++ }},
		{"module redelivered", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) { (*rows)[0].Generation++ }},
		{"delivery digest changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].DeliveryDigest = "other"
		}},
		{"scope narrowed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].ScopeDigest = "narrower"
		}},
		{"standing role changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].RoleID = "other"
		}},
		{"module receipt operation changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].ConsentOperationID = "other"
		}},
		{"module receipt intent changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].ConsentIntent = "other"
		}},
		{"module receipt revision changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].ConsentRevision++
		}},
		{"envelope changed", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].EnvelopeRevision++
		}},
		{"replacement target", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].TargetID = "replacement"
		}},
		{"foreign organization", func(_ *domain.Installation, rows *[]business.ModuleInstallationPrerequisite) {
			(*rows)[0].Organization = "other"
		}},
		{"superseded operation", func(i *domain.Installation, _ *[]business.ModuleInstallationPrerequisite) {
			i.Consent.OperationID = "other"
		}},
		{"changed caller intent", func(i *domain.Installation, _ *[]business.ModuleInstallationPrerequisite) { i.Consent.Intent = "other" }},
		{"canonical revision changed", func(i *domain.Installation, _ *[]business.ModuleInstallationPrerequisite) { i.Revision++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := inst
			rows := []business.ModuleInstallationPrerequisite{module}
			tc.change(&current, &rows)
			if err := plan.ValidateActivation(current, rows, plan.SetupJobID, true); err == nil {
				t.Fatal("changed prerequisites were accepted for activation")
			}
		})
	}
	if err := (business.SolutionInstallationPlan{}).ValidateActivation(inst, nil, "", true); err == nil {
		t.Fatal("missing plan was accepted")
	}
	for _, job := range []string{"", "previous-attempt-job"} {
		if err := plan.ValidateActivation(inst, []business.ModuleInstallationPrerequisite{module}, job, true); err == nil {
			t.Fatal("missing or foreign setup acknowledgement was accepted")
		}
	}
	// A target without an install step (a module page) activates with no
	// setup job at all, and a job queued for one is a changed attempt.
	stepless := plan
	stepless.SetupJobID = ""
	if err := stepless.ValidateActivation(inst, []business.ModuleInstallationPrerequisite{module}, "", false); err != nil {
		t.Fatal(err)
	}
	if err := plan.ValidateActivation(inst, []business.ModuleInstallationPrerequisite{module}, plan.SetupJobID, false); err == nil {
		t.Fatal("a setup job was accepted for a target that declares no install step")
	}
	module.NoStandingAuthority, module.RoleID = true, ""
	plan.Dependencies = []business.ModuleInstallationPrerequisite{module}
	if err := plan.ValidateActivation(inst, []business.ModuleInstallationPrerequisite{module}, plan.SetupJobID, true); err != nil {
		t.Fatal(err)
	}
	module.RoleID = "unexpected-standing-grant"
	if err := plan.ValidateActivation(inst, []business.ModuleInstallationPrerequisite{module}, plan.SetupJobID, true); err == nil {
		t.Fatal("a declared no-standing module acquired a grant")
	}
}

// Whether a target is set up through an install step is read from its
// declaration in one place: a solution is, a module page is not.
func TestDeclaresInstallStepFollowsTheDeclaration(t *testing.T) {
	viewer := map[string][]*basev0.WorkScopeV1{"viewer": {{ResourceKind: "example.note", Actions: []string{"read"}}}}
	if !business.DeclaresInstallStep(domain.Module{Name: "example", Kind: domain.KindSolution, Roles: viewer}) {
		t.Fatal("a solution is set up by its install step")
	}
	if business.DeclaresInstallStep(domain.Module{Name: "example", Kind: domain.KindModule, Roles: viewer}) {
		t.Fatal("a module page declares no install endpoint")
	}
}
