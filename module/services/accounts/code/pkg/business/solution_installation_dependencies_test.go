package business_test

import (
	"reflect"
	"testing"

	"accounts/pkg/business"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
)

func TestSolutionDependencyReviewUsesSignedIdentitiesAndPreservesOperations(t *testing.T) {
	invoke := []*basev0.WorkScopeV1{{ResourceKind: "records", Actions: []string{"read"}}}
	lookup := []*basev0.WorkScopeV1{{ResourceKind: "records", Actions: []string{"find"}}}
	headless := []*basev0.WorkScopeV1{{ResourceKind: "records", Actions: []string{"refresh"}}}
	binding := domain.Binding{ID: "local-binding-key", Audience: "module:records", InvokeScopes: invoke, LookupScopes: lookup, HeadlessScopes: headless}
	edges := []domain.EdgeDependency{{Module: "module:annotations", Presence: "optional"}, {Module: "module:records", Presence: "required"}}
	offer := authority.ReviewedOffer{Module: domain.Module{Principal: "solution:example", Bindings: map[string]domain.Binding{
		binding.ID: binding, "self": {ID: "self", Audience: "solution:example"},
	}, EdgeDependencies: edges}, Modules: []domain.Module{
		{Name: "records-route", Principal: "module:records", Kind: domain.KindModule, Generation: 3, ResourceKinds: []*basev0.WorkScopeV1{{ResourceKind: "records", Actions: []string{"read", "find", "refresh"}}}},
	}}
	deps, err := business.SolutionInstallationDependencies(offer)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[0].Principal != "module:annotations" || deps[0].Delivered || deps[0].Required {
		t.Fatalf("optional missing dependency was invented or required: %+v", deps)
	}
	if !deps[1].Required || !deps[1].Delivered || deps[1].Module.Name != "records-route" || !reflect.DeepEqual(deps[1].Bindings, []domain.Binding{binding}) {
		t.Fatalf("binding operation distinctions or target identity changed: %+v", deps[1])
	}
	if !reflect.DeepEqual(offer.Module.EdgeDependencies, edges) {
		t.Fatal("review reordered the signed edge declaration")
	}
	for _, alias := range []string{"records-route", "local-binding-key", "unknown"} {
		copy := offer
		copy.Module.EdgeDependencies = []domain.EdgeDependency{{Module: alias, Presence: "required"}}
		if _, err := business.SolutionInstallationDependencies(copy); err == nil {
			t.Fatalf("guessed dependency from %q", alias)
		}
	}
	copy := offer
	copy.Modules = append(copy.Modules, domain.Module{Name: "replacement", Principal: "module:records", Kind: domain.KindModule})
	if _, err := business.SolutionInstallationDependencies(copy); err == nil {
		t.Fatal("ambiguous identity was selected")
	}
}

func TestPresenceOnlyDependencyCreatesNoBindingOrScope(t *testing.T) {
	offer := authority.ReviewedOffer{Module: domain.Module{EdgeDependencies: []domain.EdgeDependency{{Module: "module:records", Presence: "required"}}},
		Modules: []domain.Module{{Name: "records-route", Principal: "module:records", Kind: domain.KindModule}}}
	deps, err := business.SolutionInstallationDependencies(offer)
	if err != nil || len(deps) != 1 || len(deps[0].Bindings) != 0 {
		t.Fatalf("edge created authority: %+v %v", deps, err)
	}
}

func TestSolutionDependencyIncomingAccessUsesSharedOperationDecisions(t *testing.T) {
	for _, operation := range []string{"invoke", "lookup", "headless"} {
		t.Run(operation, func(t *testing.T) {
			required := []*basev0.WorkScopeV1{{ResourceKind: "records", Actions: []string{"read"}, ResourceIds: []string{"reviewed"}}}
			binding := domain.Binding{ID: "read", Audience: "module:records"}
			switch operation {
			case "invoke":
				binding.InvokeScopes = required
			case "lookup":
				binding.LookupScopes = required
			case "headless":
				binding.HeadlessScopes = required
			}
			module := domain.Module{Principal: "module:records", Kind: domain.KindModule, ResourceKinds: required}
			offer := authority.ReviewedOffer{Module: domain.Module{Bindings: map[string]domain.Binding{"read": binding}}, Modules: []domain.Module{module}}
			if _, err := business.SolutionInstallationDependencies(offer); err != nil {
				t.Fatal(err)
			}
			offer.Modules[0].ResourceKinds = []*basev0.WorkScopeV1{{ResourceKind: "records", Actions: []string{"read"}, ResourceIds: []string{"another"}}}
			if _, err := business.SolutionInstallationDependencies(offer); err == nil {
				t.Fatal("incompatible incoming resource scope accepted")
			}
			offer.Modules[0].ResourceKinds = nil
			if _, err := business.SolutionInstallationDependencies(offer); err == nil {
				t.Fatal("absent served access accepted")
			}
		})
	}
}

func TestFinalDependencyFenceRejectsOmittedOrUnrelatedWitness(t *testing.T) {
	offer := authority.ReviewedOffer{Module: domain.Module{Principal: "solution:example",
		Bindings:         map[string]domain.Binding{"call": {ID: "call", Audience: "module:records"}},
		EdgeDependencies: []domain.EdgeDependency{{Module: "module:annotations", Presence: "optional"}}},
		Modules: []domain.Module{{Name: "records-route", Principal: "module:records", Kind: domain.KindModule, Generation: 4},
			{Name: "notes-route", Principal: "module:annotations", Kind: domain.KindModule, Generation: 2}}}
	witness := business.ModuleInstallationPrerequisite{PrincipalID: business.ModulePrincipalID("module:records"), Generation: 4}
	plan := business.SolutionInstallationPlan{Dependencies: []business.ModuleInstallationPrerequisite{witness}}
	if err := plan.ValidateDependencySet(offer); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		rows []business.ModuleInstallationPrerequisite
	}{
		{"omitted required module", nil},
		{"route alias substituted", []business.ModuleInstallationPrerequisite{{PrincipalID: business.ModulePrincipalID("records-route"), Generation: 4}}},
		{"binding id substituted", []business.ModuleInstallationPrerequisite{{PrincipalID: business.ModulePrincipalID("call"), Generation: 4}}},
		{"old generation", []business.ModuleInstallationPrerequisite{{PrincipalID: witness.PrincipalID, Generation: 3}}},
		{"duplicate module", []business.ModuleInstallationPrerequisite{witness, witness}},
		{"extra module", []business.ModuleInstallationPrerequisite{witness, {PrincipalID: business.ModulePrincipalID("module:unrelated"), Generation: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := business.SolutionInstallationPlan{Dependencies: tc.rows}
			if err := p.ValidateDependencySet(offer); err == nil {
				t.Fatal("incomplete or changed graph accepted")
			}
		})
	}
	plan.Dependencies = append(plan.Dependencies, business.ModuleInstallationPrerequisite{PrincipalID: business.ModulePrincipalID("module:annotations"), Generation: 2})
	if err := plan.ValidateDependencySet(offer); err != nil {
		t.Fatal(err)
	}
	offer.Module.EdgeDependencies[0].Presence = "required"
	if err := (business.SolutionInstallationPlan{Dependencies: []business.ModuleInstallationPrerequisite{witness}}).ValidateDependencySet(offer); err == nil {
		t.Fatal("required edge module omitted")
	}
}
