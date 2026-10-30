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
func SolutionInstallationDependencies(offer authority.ReviewedOffer) ([]SolutionDependency, error) {
	modules := make(map[string]domain.Module, len(offer.Modules))
	for _, m := range offer.Modules {
		if _, duplicate := modules[m.Principal]; duplicate {
			return nil, domain.NotNow("INSTALLATION_DEPENDENCY_AMBIGUOUS: multiple delivered targets name principal %q", m.Principal)
		}
		modules[m.Principal] = m
	}
	byPrincipal := map[string]*SolutionDependency{}
	add := func(principal string, required bool) (*SolutionDependency, error) {
		if existing := byPrincipal[principal]; existing != nil {
			existing.Required = existing.Required || required
			if existing.Required && !existing.Delivered {
				return nil, domain.NotNow("INSTALLATION_DEPENDENCY_MISSING: required module identity %q is not delivered", principal)
			}
			return existing, nil
		}
		m, delivered := modules[principal]
		if delivered && m.Kind != domain.KindModule {
			return nil, domain.NotNow("INSTALLATION_DEPENDENCY_KIND: %q does not identify a supporting module", principal)
		}
		if required && !delivered {
			return nil, domain.NotNow("INSTALLATION_DEPENDENCY_MISSING: required module identity %q is not delivered", principal)
		}
		entry := &SolutionDependency{Principal: principal, Required: required, Delivered: delivered, Module: m}
		byPrincipal[principal] = entry
		return entry, nil
	}
	for _, id := range slices.Sorted(maps.Keys(offer.Module.Bindings)) {
		binding := offer.Module.Bindings[id]
		if binding.Audience == offer.Module.Principal {
			continue
		}
		entry, err := add(binding.Audience, true)
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
	}
	for _, edge := range offer.Module.EdgeDependencies {
		if _, err := add(edge.Module, edge.Presence == "required"); err != nil {
			return nil, err
		}
	}
	out := make([]SolutionDependency, 0, len(byPrincipal))
	for _, principal := range slices.Sorted(maps.Keys(byPrincipal)) {
		out = append(out, *byPrincipal[principal])
	}
	return out, nil
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
