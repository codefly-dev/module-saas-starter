package business

import (
	"github.com/codefly-dev/core/solutionhost"
)

// Admission of a delivered desired set (issue #952).
//
// core is the judge and the whole judge: solutionhost.Admit decides a
// manifest's generation against the one this host applied for its binding
// (stale, rewritten, withdrawn, another host or domain), and
// solutionhost.CheckAlias decides its route alias against every applied
// manifest. What remains here is the part that is this host's: asking core
// about each delivered manifest and reading the answers into the shape the
// reconcile pass applies.

// SolutionHostBindingDecision is one admitted manifest and what the host should
// do with it.
type SolutionHostBindingDecision struct {
	// Delivered is the attested carrier this decision was made on. The apply
	// re-admits inside its own transaction from the verified value, never from
	// the manifest read out of it.
	Delivered *solutionhost.Delivered
	Document  *solutionhost.ModuleManifest
	Decision  solutionhost.Decision
}

// solutionHostBindingAdmission is the outcome of one pass's admission: the
// manifests to reconcile, and the reason each withheld binding was withheld.
type solutionHostBindingAdmission struct {
	Accepted []SolutionHostBindingDecision
	Withheld map[string]string
}

// admitSolutionHostBindings asks core about each delivered manifest, against
// the applied record of its own binding and the aliases every applied manifest
// holds, and sorts the answers into what to apply and what to expose as
// refused. Nothing core refused is applied.
//
// Two candidates claiming one alias in the same pass both pass CheckAlias,
// which judges against what is APPLIED; the second apply then meets the
// registry's durable backstop (ErrSolutionHostBindingRouteHeld) and is
// recorded as refused, so a pass never applies both.
func admitSolutionHostBindings(
	coordinate string, records []*SolutionHostBindingRecord, delivered []deliveredSolutionHostBinding,
) solutionHostBindingAdmission {
	result := solutionHostBindingAdmission{Withheld: map[string]string{}}
	byBinding := make(map[string]*SolutionHostBindingRecord, len(records))
	for _, record := range records {
		byBinding[record.BindingID] = record
	}
	applied := appliedManifests(records)
	reserved := []string{SolutionHostReservedRouteNamespace}
	for _, one := range delivered {
		candidate := one.Document
		previous, err := byBinding[candidate.Binding].AppliedManifest()
		var decision solutionhost.Decision
		if err == nil {
			decision, err = solutionhost.Admit(coordinate, previous, candidate)
		}
		if err == nil {
			err = solutionhost.CheckAlias(reserved, applied, candidate)
		}
		if err != nil {
			result.Withheld[candidate.Binding] = err.Error()
			continue
		}
		result.Accepted = append(result.Accepted, SolutionHostBindingDecision{
			Delivered: one.Delivered, Document: candidate, Decision: decision,
		})
	}
	return result
}
