package business

import (
	"fmt"
	"strings"
)

// ModuleNamespaceRule is how this cell spells the namespace a module's
// workloads run in, so a reviewed (namespace, ServiceAccount) pair reads back
// to a manifest's (module, service) and a module reads forward to the
// namespace its delivery carrier runs in.
//
// A manifest declares no namespace: core's rule is that a cell runs a
// module's workloads in the namespace named after the module, as the
// ServiceAccount named after the service (workloadidentity). A cell that
// composes several modules spells that namespace with its environment's
// prefix, "<environment namespace>-<module>". This host is itself a workload of
// the cell, placed by the same rule, so the prefix is read off its OWN
// placement — its namespace and its module — rather than configured: a value
// a deployer could type is a value that could disagree with the cell.
type ModuleNamespaceRule struct {
	prefix string
}

// NewModuleNamespaceRule derives the rule from where this host runs: the
// namespace it runs in and the module it was composed as. A namespace that is
// the module's name is core's bare rule; one ending in "-<module>" carries the
// environment's prefix; anything else is a placement no rule explains, and a
// host that cannot read its own placement cannot read anyone else's.
func NewModuleNamespaceRule(hostNamespace, hostModule string) (ModuleNamespaceRule, error) {
	if hostNamespace == "" || hostModule == "" {
		return ModuleNamespaceRule{}, fmt.Errorf("the module namespace rule needs this host's namespace and module, and got %q and %q", hostNamespace, hostModule)
	}
	if hostNamespace == hostModule {
		return ModuleNamespaceRule{}, nil
	}
	prefix, found := strings.CutSuffix(hostNamespace, "-"+hostModule)
	if !found || prefix == "" {
		return ModuleNamespaceRule{}, fmt.Errorf("this host runs as module %q in namespace %q, which is neither the module's name nor \"<environment namespace>-%s\"; no rule places a module's workloads, so none can be read back",
			hostModule, hostNamespace, hostModule)
	}
	return ModuleNamespaceRule{prefix: prefix + "-"}, nil
}

// Namespace is the namespace the cell runs module's workloads in.
func (r ModuleNamespaceRule) Namespace(module string) string { return r.prefix + module }

// SetModuleNamespaceRule wires the rule. Boot wiring; unset is core's bare
// rule, the namespace named after the module.
func (s *Service) SetModuleNamespaceRule(rule ModuleNamespaceRule) { s.moduleNamespaces = rule }
