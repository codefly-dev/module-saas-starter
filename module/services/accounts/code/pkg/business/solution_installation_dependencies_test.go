package business_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"accounts/pkg/business"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
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

// A solution that binds a module needs what that module itself reaches, or the
// module's calls there are refused the moment the solution is active: the pages
// solution binds documents, documents embeds through model, so model is a
// required dependency of the pages installation although pages never names it.
func TestSolutionDependenciesCloseOverWhatSupportingModulesReach(t *testing.T) {
	read := []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}}
	embed := []*basev0.WorkScopeV1{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke"}}}
	rows := []*basev0.WorkScopeV1{{ResourceKind: "records.records", Actions: []string{"read"}}}
	documents := domain.Module{Name: "documents", Principal: "module:documents", Kind: domain.KindModule, Generation: 5,
		ResourceKinds: read, Bindings: map[string]domain.Binding{
			"model": {ID: "model", Audience: "module:model", InvokeScopes: embed},
			"self":  {ID: "self", Audience: "module:documents"},
			"pages": {ID: "pages", Audience: "solution:pages", InvokeScopes: read},
			"other": {ID: "other", Audience: "solution:other", InvokeScopes: read},
		}, EdgeDependencies: []domain.EdgeDependency{
			{Module: "module:records", Presence: "optional"},
			{Module: "solution:other", Presence: "required"},
			{Module: "solution:pages", Presence: "required"},
		}}
	model := domain.Module{Name: "model", Principal: "module:model", Kind: domain.KindModule, Generation: 4, ResourceKinds: embed,
		Bindings: map[string]domain.Binding{"documents": {ID: "documents", Audience: "module:documents", InvokeScopes: read}}}
	records := domain.Module{Name: "records", Principal: "module:records", Kind: domain.KindModule, Generation: 2, ResourceKinds: rows,
		Bindings: map[string]domain.Binding{"org-directory": {ID: "org-directory", Audience: "module:org-directory", InvokeScopes: rows}}}
	pages := domain.Module{Name: "pages", Principal: "solution:pages", Kind: domain.KindSolution, Generation: 3,
		Bindings: map[string]domain.Binding{"documents": {ID: "documents", Audience: "module:documents", InvokeScopes: read}}}
	other := domain.Module{Name: "other", Principal: "solution:other", Kind: domain.KindSolution, Generation: 1}
	offer := authority.ReviewedOffer{Module: pages, Modules: []domain.Module{documents, model, records, pages, other}}

	deps, err := business.SolutionInstallationDependencies(offer)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]business.SolutionDependency{}
	for _, d := range deps {
		got[d.Principal] = d
	}
	if len(got) != 4 {
		t.Fatalf("expected documents, model, records and org-directory: %+v", deps)
	}
	if d := got["module:model"]; !d.Required || !d.Delivered || d.Module.Generation != 4 || len(d.Bindings) != 0 || d.Via != "module:documents" {
		t.Fatalf("model reached through documents is not a required, binding-free dependency named through documents: %+v", d)
	}
	if d := got["module:documents"]; !d.Required || len(d.Bindings) != 1 || d.Via != "" {
		t.Fatalf("the solution's own binding onto documents was lost: %+v", d)
	}
	if d := got["module:records"]; d.Required || !d.Delivered {
		t.Fatalf("an optional edge of a supporting module became required: %+v", d)
	}
	if d := got["module:org-directory"]; d.Required || d.Delivered {
		t.Fatalf("a module behind an optional edge was required or invented: %+v", d)
	}
	for _, principal := range []string{"solution:pages", "solution:other"} {
		if _, present := got[principal]; present {
			t.Fatalf("a supporting module reaching solution %q made it a dependency", principal)
		}
	}

	// A module's binding onto an audience this host has not delivered is shown
	// absent and never refused: the module's declaration is fleet-wide, the
	// catalogue is this host's. The solution's own binding keeps refusing.
	undelivered := offer
	undelivered.Modules = []domain.Module{documents, records, pages, other}
	deps, err = business.SolutionInstallationDependencies(undelivered)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range deps {
		if d.Principal == "module:model" {
			found = true
			if d.Required || d.Delivered || d.Via != "module:documents" {
				t.Fatalf("documents' reach onto an undelivered model was required or invented: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("documents' reach onto an undelivered model was not shown")
	}
	// A supporting module's required edge keeps its declared presence.
	fenced := offer
	fencedDocuments := documents
	fencedDocuments.EdgeDependencies = []domain.EdgeDependency{{Module: "module:records", Presence: "required"}}
	fenced.Modules = []domain.Module{fencedDocuments, model, pages, other}
	if _, err := business.SolutionInstallationDependencies(fenced); err == nil {
		t.Fatal("documents' required edge onto an undelivered records was accepted")
	} else if !strings.Contains(err.Error(), `"module:records" (reached through "module:documents")`) {
		t.Fatalf("the refusal does not name the path: %v", err)
	}
	// An optional reach that a later required path promotes walks that
	// module's own reach as required.
	promoted := offer
	promoted.Module.EdgeDependencies = []domain.EdgeDependency{{Module: "module:records", Presence: "required"}}
	orgDirectory := domain.Module{Name: "org-directory", Principal: "module:org-directory", Kind: domain.KindModule, Generation: 3, ResourceKinds: rows}
	promoted.Modules = append(slices.Clone(offer.Modules), orgDirectory)
	deps, err = business.SolutionInstallationDependencies(promoted)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Principal == "module:org-directory" && !d.Required {
			t.Fatalf("records promoted to required did not carry org-directory with it: %+v", d)
		}
	}
	// The activation fence sees the same closure: a plan that witnessed only
	// what the solution names refuses on the module it reaches through them.
	plan := business.SolutionInstallationPlan{Dependencies: []business.ModuleInstallationPrerequisite{{PrincipalID: business.ModulePrincipalID("module:documents"), Generation: 5}}}
	if err := plan.ValidateDependencySet(offer); err == nil || !strings.Contains(err.Error(), `required module "module:model" (reached through "module:documents") has no consent witness`) {
		t.Fatalf("the activation fence missed the transitive module: %v", err)
	}
}

// The host's walker and the core's closure (decide.Closure) are one rule: what
// the review lists as required is exactly what installing the target brings
// along. Every module is installed by composition, so a module page (a module
// declaring roles) reached by the target's own binding or edge, or through a
// supporting module's, is listed and walked through like any module; only a
// solution reached is chosen by its own review and left out.
func TestTheWalkersClosureIsTheCoresClosure(t *testing.T) {
	read := []*basev0.WorkScopeV1{{ResourceKind: "support", Actions: []string{"read"}}}
	entries := []*basev0.WorkScopeV1{{ResourceKind: "directory.entry", Actions: []string{"read"}}}
	page := domain.Module{Name: "directory", Principal: "module:directory", Kind: domain.KindModule, Generation: 2, ResourceKinds: entries,
		InstallationRole: domain.NoInstallationRole, Roles: map[string][]*basev0.WorkScopeV1{"viewer": entries},
		Bindings: map[string]domain.Binding{"behind": {ID: "behind", Audience: "module:behind-page"}}}
	behindPage := domain.Module{Name: "behind-page", Principal: "module:behind-page", Kind: domain.KindModule, Generation: 1}
	model := domain.Module{Name: "model", Principal: "module:model", Kind: domain.KindModule, Generation: 4}
	support := domain.Module{Name: "support", Principal: "module:support", Kind: domain.KindModule, Generation: 3, ResourceKinds: read,
		Bindings: map[string]domain.Binding{
			"directory": {ID: "directory", Audience: "module:directory", InvokeScopes: entries},
			"model":     {ID: "model", Audience: "module:model"},
		}}
	edged := domain.Module{Name: "edged", Principal: "module:edged", Kind: domain.KindModule, Generation: 1,
		EdgeDependencies: []domain.EdgeDependency{{Module: "module:directory", Presence: "required"}, {Module: "module:model", Presence: "required"}}}
	optional := domain.Module{Name: "optional", Principal: "module:optional", Kind: domain.KindModule, Generation: 1,
		Bindings: map[string]domain.Binding{"model": {ID: "model", Audience: "module:model"}}}
	app := domain.Module{Name: "app", Principal: "solution:app", Kind: domain.KindSolution, Generation: 1,
		Bindings: map[string]domain.Binding{
			"support":   {ID: "support", Audience: "module:support", InvokeScopes: read},
			"directory": {ID: "directory", Audience: "module:directory", InvokeScopes: entries},
		},
		EdgeDependencies: []domain.EdgeDependency{{Module: "module:edged", Presence: "required"}, {Module: "module:directory", Presence: "required"}, {Module: "module:optional", Presence: "optional"}}}
	composed := []domain.Module{page, behindPage, model, support, edged, optional}

	for name, target := range map[string]domain.Module{
		"a solution reaching a module page by its own binding and edge, and through a module's binding and edge": app,
		"a solution reaching it only through a supporting module's binding": {Name: "through-binding", Principal: "solution:through-binding", Kind: domain.KindSolution, Generation: 1,
			Bindings: map[string]domain.Binding{"support": {ID: "support", Audience: "module:support", InvokeScopes: read}}},
		"a solution reaching it only through a supporting module's edge": {Name: "through-edge", Principal: "solution:through-edge", Kind: domain.KindSolution, Generation: 1,
			EdgeDependencies: []domain.EdgeDependency{{Module: "module:edged", Presence: "required"}}},
		"a module page reaching another module page": {Name: "other-page", Principal: "module:other-page", Kind: domain.KindModule, Generation: 1,
			InstallationRole: domain.NoInstallationRole, Roles: map[string][]*basev0.WorkScopeV1{"viewer": read},
			Bindings: map[string]domain.Binding{"support": {ID: "support", Audience: "module:support", InvokeScopes: read}, "directory": {ID: "directory", Audience: "module:directory", InvokeScopes: entries}}},
	} {
		t.Run(name, func(t *testing.T) {
			modules := append(slices.Clone(composed), target)
			offer := authority.ReviewedOffer{Module: target, Modules: modules}
			core, _, err := decide.Closure(target, modules)
			if err != nil {
				t.Fatal(err)
			}
			deps, err := business.SolutionInstallationDependencies(offer)
			if err != nil {
				t.Fatal(err)
			}
			var walked []string
			for _, d := range deps {
				if d.Required && d.Delivered {
					walked = append(walked, d.Module.Name)
				}
			}
			slices.Sort(walked)
			if !slices.Equal(walked, core) {
				t.Fatalf("the walker found %v, the core's closure is %v", walked, core)
			}
			if len(core) == 0 {
				t.Fatal("the fixture reaches no supporting module: the comparison proves nothing")
			}
		})
	}
	// The fixtures the other walker cases use agree as well.
	documents := domain.Module{Name: "documents", Principal: "module:documents", Kind: domain.KindModule, Generation: 5,
		Bindings:         map[string]domain.Binding{"model": {ID: "model", Audience: "module:model"}, "pages": {ID: "pages", Audience: "solution:pages"}},
		EdgeDependencies: []domain.EdgeDependency{{Module: "module:records", Presence: "optional"}, {Module: "solution:pages", Presence: "required"}}}
	records := domain.Module{Name: "records", Principal: "module:records", Kind: domain.KindModule, Generation: 2,
		Bindings: map[string]domain.Binding{"directory": {ID: "directory", Audience: "module:directory"}}}
	pages := domain.Module{Name: "pages", Principal: "solution:pages", Kind: domain.KindSolution, Generation: 3,
		Bindings:         map[string]domain.Binding{"documents": {ID: "documents", Audience: "module:documents"}},
		EdgeDependencies: []domain.EdgeDependency{{Module: "module:records", Presence: "required"}}}
	modules := []domain.Module{documents, model, records, pages, page, behindPage}
	core, _, err := decide.Closure(pages, modules)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := business.SolutionInstallationDependencies(authority.ReviewedOffer{Module: pages, Modules: modules})
	if err != nil {
		t.Fatal(err)
	}
	var walked []string
	for _, d := range deps {
		if d.Required && d.Delivered {
			walked = append(walked, d.Module.Name)
		}
	}
	slices.Sort(walked)
	if want := []string{"behind-page", "directory", "documents", "model", "records"}; !slices.Equal(walked, core) || !slices.Equal(core, want) {
		t.Fatalf("the walker found %v, the core's closure is %v, want %v", walked, core, want)
	}
}
