package main

import (
	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
)

// Read APPLIED presence only. Desired documents do not authorize traffic. The
// registration, applied generation and parsed document must name the same
// binding and generation. Presence can declare several workloads; until it
// identifies which serves an endpoint, only one distinct identity is routable.
func appliedWorkloadSPIFFEID(binding *accountsv1.SolutionHostBindingState) string {
	applied := binding.GetApplied()
	record := binding.GetRegistration()
	declared := record.GetDeclared()
	if applied == nil || applied.GetRemoved() || declared == nil ||
		declared.GetBindingId() != binding.GetBindingId() ||
		declared.GetGeneration() != applied.GetGeneration().GetGeneration() ||
		applied.GetSolutionId() != record.GetSolutionId() || record.GetTombstonedAt() != nil {
		return ""
	}
	document, err := solutionhost.Parse([]byte(applied.GetGeneration().GetDocument()))
	if err != nil || document.Binding != declared.GetBindingId() || document.Generation != declared.GetGeneration() {
		return ""
	}
	id := ""
	for _, workload := range document.Workloads {
		candidate := workload.Identity.SPIFFEID
		if !workloadSPIFFEPattern.MatchString(candidate) || (id != "" && id != candidate) {
			return ""
		}
		id = candidate
	}
	return id
}
