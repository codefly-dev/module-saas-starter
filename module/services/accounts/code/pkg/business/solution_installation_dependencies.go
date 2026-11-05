package business

import (
	"maps"
	"slices"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	corescope "github.com/codefly-dev/authority/libraries/authority-core/go/scope"
)

// SolutionDependency is a review requirement, not consent or a grant. Bindings
// retain their separate invoke/lookup/headless scopes; an edge contributes only
// presence. An absent optional module is shown as such, never installed by guess.
//
// Bindings holds the solution's own bindings onto the module: what the review
// shows the administrator the solution asks there. A module reached only
// through another module carries none, because what that module asks is its
// own applied declaration, granted at its own authority root.
type SolutionDependency struct {
	Principal string
	Required  bool
	Delivered bool
	Module    domain.Module
	Bindings  []domain.Binding
}

// SolutionInstallationDependencies resolves signed audience identities through
// the shared offer's delivered catalogue. Neither a binding ID nor a route
// alias is a substitute for that identity. It leaves the declaration untouched,
// including edge order, and returns a deterministic presentation of its union.
//
// The union closes over what each supporting module itself reaches: a solution
// that binds documents needs the model documents embeds with, or every call
// documents makes there is refused once the solution is active. A module's own
// bindings and edges are walked exactly as the solution's are, under the
// presence of the path that reached it; a path through an optional module makes
// nothing required. A module reaching a solution, or a target that is not a
// supporting module, contributes nothing: a solution installs on its own review.
func SolutionInstallationDependencies(offer authority.ReviewedOffer) ([]SolutionDependency, error) {
	modules := make(map[string]domain.Module, len(offer.Modules))
	for _, m := range offer.Modules {
		if _, duplicate := modules[m.Principal]; duplicate {
			return nil, domain.NotNow("INSTALLATION_DEPENDENCY_AMBIGUOUS: multiple delivered targets name principal %q", m.Principal)
		}
		modules[m.Principal] = m
	}
	byPrincipal := map[string]*SolutionDependency{}
	// add records a dependency and says whether its reach must (still) be
	// walked: on first sight, and again when a later path makes it required.
	add := func(principal string, required bool) (*SolutionDependency, bool, error) {
		if existing := byPrincipal[principal]; existing != nil {
			raised := required && !existing.Required
			existing.Required = existing.Required || required
			if existing.Required && !existing.Delivered {
				return nil, false, domain.NotNow("INSTALLATION_DEPENDENCY_MISSING: required module identity %q is not delivered", principal)
			}
			return existing, raised, nil
		}
		m, delivered := modules[principal]
		if delivered && m.Kind != domain.KindModule {
			return nil, false, domain.NotNow("INSTALLATION_DEPENDENCY_KIND: %q does not identify a supporting module", principal)
		}
		if required && !delivered {
			return nil, false, domain.NotNow("INSTALLATION_DEPENDENCY_MISSING: required module identity %q is not delivered", principal)
		}
		entry := &SolutionDependency{Principal: principal, Required: required, Delivered: delivered, Module: m}
		byPrincipal[principal] = entry
		return entry, delivered, nil
	}
	type reach struct {
		module   domain.Module
		required bool
	}
	var pending []reach
	for _, id := range slices.Sorted(maps.Keys(offer.Module.Bindings)) {
		binding := offer.Module.Bindings[id]
		if binding.Audience == offer.Module.Principal {
			continue
		}
		entry, walk, err := add(binding.Audience, true)
		if err != nil {
			return nil, err
		}
		for _, operation := range []string{"invoke", "lookup", "headless"} {
			required, err := decide.ScopesFor(binding, operation, operation)
			if err != nil {
				return nil, err
			}
			if binding.Audience != entry.Module.Principal || !corescope.Attenuates(required, decide.Served(entry.Module)) {
				return nil, domain.NotNow("INSTALLATION_DEPENDENCY_ACCESS: module %q does not serve the reviewed %s access of binding %q", binding.Audience, operation, binding.ID)
			}
		}
		entry.Bindings = append(entry.Bindings, binding)
		if walk {
			pending = append(pending, reach{entry.Module, true})
		}
	}
	for _, edge := range offer.Module.EdgeDependencies {
		entry, walk, err := add(edge.Module, edge.Presence == "required")
		if err != nil {
			return nil, err
		}
		if walk {
			pending = append(pending, reach{entry.Module, entry.Required})
		}
	}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]
		for _, principal := range reachedModules(next.module, offer.Module.Principal) {
			target, delivered := modules[principal]
			if delivered && target.Kind != domain.KindModule {
				continue // A solution installs on its own review.
			}
			entry, walk, err := add(principal, next.required)
			if err != nil {
				return nil, err
			}
			if walk {
				pending = append(pending, reach{entry.Module, next.required})
			}
		}
		for _, edge := range next.module.EdgeDependencies {
			target, delivered := modules[edge.Module]
			if delivered && target.Kind != domain.KindModule {
				continue
			}
			entry, walk, err := add(edge.Module, next.required && edge.Presence == "required")
			if err != nil {
				return nil, err
			}
			if walk {
				pending = append(pending, reach{entry.Module, entry.Required})
			}
		}
	}
	out := make([]SolutionDependency, 0, len(byPrincipal))
	for _, principal := range slices.Sorted(maps.Keys(byPrincipal)) {
		out = append(out, *byPrincipal[principal])
	}
	return out, nil
}

// reachedModules lists the audiences a module's own bindings name, in binding
// ID order, without the module itself or the solution under review.
func reachedModules(m domain.Module, solution string) []string {
	var out []string
	for _, id := range slices.Sorted(maps.Keys(m.Bindings)) {
		audience := m.Bindings[id].Audience
		if audience == m.Principal || audience == solution || slices.Contains(out, audience) {
			continue
		}
		out = append(out, audience)
	}
	return out
}

// ValidateDependencySet checks completeness against the delivered graph read in
// the activation transaction. Comparing only the witnesses a plan supplied would
// accept a plan that silently omitted a required dependency. PrincipalID is the
// host's UUID projection; the signed principal itself is never a host target ID.
func (p SolutionInstallationPlan) ValidateDependencySet(offer authority.ReviewedOffer) error {
	dependencies, err := SolutionInstallationDependencies(offer)
	if err != nil {
		return err
	}
	remaining := make(map[string]ModuleInstallationPrerequisite, len(p.Dependencies))
	for _, witness := range p.Dependencies {
		if _, duplicate := remaining[witness.PrincipalID]; duplicate {
			return domain.NotNow("INSTALLATION_PREREQUISITES_CHANGED: a module identity occurs more than once")
		}
		remaining[witness.PrincipalID] = witness
	}
	for _, dependency := range dependencies {
		principal := ModulePrincipalID(dependency.Principal)
		witness, present := remaining[principal]
		if !present {
			if dependency.Required {
				return domain.NotNow("INSTALLATION_PREREQUISITES_CHANGED: required module %q has no consent witness", dependency.Principal)
			}
			continue
		}
		if !dependency.Delivered || witness.Generation != dependency.Module.Generation {
			return domain.NotNow("INSTALLATION_PREREQUISITES_CHANGED: module %q no longer has its reviewed delivery", dependency.Principal)
		}
		delete(remaining, principal)
	}
	if len(remaining) != 0 {
		return domain.NotNow("INSTALLATION_PREREQUISITES_CHANGED: the plan contains a module outside the delivered dependency graph")
	}
	return nil
}
