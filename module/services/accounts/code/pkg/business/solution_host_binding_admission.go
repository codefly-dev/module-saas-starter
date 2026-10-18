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
// the applied record of its own binding (Admit) and the aliases the applied
// manifests hold (CheckAlias), and sorts the answers into what to apply and
// what to expose as refused. Nothing core refused is applied.
//
// An applied manifest whose binding this pass admits a withdrawal or a new
// alias for holds nothing: that is what lets an alias move from one binding to
// another in one pass. Refusing such a candidate changes the question — its
// binding's applied alias is held again — so the alias check repeats until no
// candidate is refused. Two new claimants of one alias both pass this check,
// which judges what is applied; the second apply meets the registry's durable
// backstop (ErrSolutionHostBindingRouteHeld) and is recorded as refused.
func admitSolutionHostBindings(
	coordinate string, records []*SolutionHostBindingRecord, delivered []deliveredSolutionHostBinding,
) solutionHostBindingAdmission {
	result := solutionHostBindingAdmission{Withheld: map[string]string{}}
	byBinding := make(map[string]*SolutionHostBindingRecord, len(records))
	for _, record := range records {
		byBinding[record.BindingID] = record
	}
	for _, one := range delivered {
		candidate := one.Document
		previous, err := byBinding[candidate.Binding].AppliedManifest()
		var decision solutionhost.Decision
		if err == nil {
			decision, err = solutionhost.Admit(coordinate, previous, candidate)
		}
		if err != nil {
			result.Withheld[candidate.Binding] = err.Error()
			continue
		}
		result.Accepted = append(result.Accepted, SolutionHostBindingDecision{
			Delivered: one.Delivered, Document: candidate, Decision: decision,
		})
	}
	applied := appliedManifests(records)
	reserved := []string{SolutionHostReservedRouteNamespace}
	for refused := true; refused; {
		refused = false
		holders := aliasHolders(applied, acceptedDocuments(result.Accepted))
		kept := result.Accepted[:0]
		for _, accepted := range result.Accepted {
			if err := solutionhost.CheckAlias(reserved, holders, accepted.Document); err != nil {
				result.Withheld[accepted.Document.Binding] = err.Error()
				refused = true
				continue
			}
			kept = append(kept, accepted)
		}
		result.Accepted = kept
	}
	return result
}

// aliasHolders is the applied manifests that still hold their alias once the
// given manifests stand in for their bindings: one whose replacement is a
// withdrawal, or names another alias, has released its own.
func aliasHolders(applied []*solutionhost.ModuleManifest, replacements map[string]*solutionhost.ModuleManifest) []*solutionhost.ModuleManifest {
	holders := make([]*solutionhost.ModuleManifest, 0, len(applied))
	for _, manifest := range applied {
		if replacement, replaced := replacements[manifest.Binding]; replaced && (replacement.Removed || replacement.Alias != manifest.Alias) {
			continue
		}
		holders = append(holders, manifest)
	}
	return holders
}

func acceptedDocuments(accepted []SolutionHostBindingDecision) map[string]*solutionhost.ModuleManifest {
	documents := make(map[string]*solutionhost.ModuleManifest, len(accepted))
	for _, one := range accepted {
		documents[one.Document.Binding] = one.Document
	}
	return documents
}
