package business

import (
	"fmt"
	"sort"

	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
)

// Admission of a delivered desired set (issue #952).
//
// core's solutionhost.Host.Admit is the judge, and it judges the set as a whole
// because two of its rules are not properties of one document: a route alias is
// unique within a host, and a binding may be declared only once per set. The host
// runs it over the whole mounted set on every pass, which is what this file
// drives.
//
// Admit answers with one error for the whole set, and that is the right answer for
// a renderer — it is refusing bytes it is about to write. A host cannot stop
// there. One stale render must not wedge every other solution on the host, and a
// stale render is the common case rather than the exotic one: the renderer derives
// a generation from the previously delivered document, so a render from a checkout
// that cannot see the prior tree emits generation 1, which this host correctly
// refuses as stale (codefly-dev/cli#853). If that refusal held back every other
// binding's new generation, one bad pipeline run would freeze the host.
//
// So admission narrows. Every document is first judged ALONE against its own
// applied generation, which settles every rule that is a property of one document
// and of what that binding has already applied. The survivors are judged together,
// and while core refuses the set, the documents participating in a route-alias
// conflict are withheld and the rest are asked again. A withheld document is not
// dropped silently: it carries the reason, exposed as its pending state.
//
// Nothing core refused is ever applied. If the set is still refused and no alias
// conflict can be attributed — which is what a rule added to a later Core would
// look like — the whole set is withheld with core's own error. Failing closed on
// an unrecognised refusal is the difference between "this host does not understand
// the new rule" and "this host silently ignored it".

// SolutionHostBindingDecision is one admitted document and what the host should do
// with it.
type SolutionHostBindingDecision struct {
	Document *solutionhost.SolutionHostBinding
	Decision solutionhost.Decision
}

// solutionHostBindingAdmission is the outcome of one pass's admission: the
// documents to reconcile, and the reason each withheld binding was withheld.
type solutionHostBindingAdmission struct {
	Accepted []SolutionHostBindingDecision
	Withheld map[string]string
}

// admitSolutionHostBindings narrows a desired set to the documents core admits,
// attributing every refusal to a binding ID.
//
// host carries this host's coordinate, its reserved route namespaces and its
// durable applied state. documents are the parsed documents of one pass.
func admitSolutionHostBindings(
	host solutionhost.Host, documents []*solutionhost.SolutionHostBinding,
) solutionHostBindingAdmission {
	result := solutionHostBindingAdmission{Withheld: map[string]string{}}

	// A binding declared twice in one set is refused by core, and the host cannot
	// attribute the refusal to one of the two: whichever it picked, it would be
	// choosing between two mounts with no rule saying which is authoritative. Both
	// are withheld and neither is judged further, so a duplicate cannot slip
	// through the per-document pass.
	delivered := map[string]int{}
	for _, document := range documents {
		delivered[document.Binding]++
	}

	applied := appliedByBinding(host.Applied)
	candidates := make([]SolutionHostBindingDecision, 0, len(documents))
	for _, document := range documents {
		if delivered[document.Binding] > 1 {
			result.Withheld[document.Binding] = fmt.Sprintf(
				"%s: binding %q is declared by %d delivered documents in one pass",
				solutionhost.ErrInvalid, document.Binding, delivered[document.Binding])
			continue
		}
		// Judged against this binding's OWN applied generation and nothing else:
		// the schema, the document's validity, the single-release invariant, this
		// host's coordinate, the reserved namespaces, and whether the generation
		// may follow the one this binding applied.
		//
		// Deliberately not against what other bindings hold. An alias is released
		// by the document of the binding holding it, in the same pass, so judging
		// one document against every applied alias would refuse the claimant of an
		// alias another delivered document is handing over. Alias conflicts are
		// attributed below, where both sides of the hand-over are visible.
		alone := solutionhost.Host{Coordinate: host.Coordinate, Reserved: host.Reserved}
		if own, found := applied[document.Binding]; found {
			alone.Applied = []solutionhost.Applied{own}
		}
		decisions, err := alone.Admit(document)
		if err != nil {
			result.Withheld[document.Binding] = err.Error()
			continue
		}
		candidates = append(candidates, SolutionHostBindingDecision{Document: document, Decision: decisions[0]})
	}

	// Ask core about the whole surviving set, and keep narrowing while it refuses.
	// Each iteration withholds at least one candidate, so this terminates in at
	// most len(candidates) rounds.
	for len(candidates) > 0 {
		_, err := host.Admit(documentsOf(candidates)...)
		if err == nil {
			break
		}
		conflicted := routeAliasConflicts(host.Applied, delivered, candidates)
		if len(conflicted) == 0 {
			// core refuses the set for a reason this host cannot attribute to any
			// document. Withhold everything rather than apply a subset it never
			// approved.
			for _, candidate := range candidates {
				result.Withheld[candidate.Document.Binding] = err.Error()
			}
			candidates = nil
			break
		}
		kept := make([]SolutionHostBindingDecision, 0, len(candidates))
		for _, candidate := range candidates {
			if reason, held := conflicted[candidate.Document.Binding]; held {
				result.Withheld[candidate.Document.Binding] = reason
				continue
			}
			kept = append(kept, candidate)
		}
		candidates = kept
	}

	result.Accepted = candidates
	return result
}

// routeAliasConflicts reports the candidates that claim a route alias something
// else also claims, and why.
//
// This is the one cross-document rule a host can attribute exactly, because it is
// computed from the claims core itself builds: an alias and its owner. Two kinds
// of owner claim an alias, and the difference matters.
//
// A CANDIDATE claims the aliases its document declares. An APPLIED binding claims
// the aliases it holds — but only while the pass is not re-deciding it: a binding
// whose own document is in the delivered set is releasing or re-claiming its
// aliases in this very pass, so counting its applied claim as well would make it
// collide with itself and would refuse the claimant of an alias it is handing over.
// That is core's own rule, and the reason it exists here is the hand-over.
//
// A withheld binding is a candidate, never an applied holder: a holder whose
// document is not in the set is not something this pass can decide. Every
// candidate claimant of a contested alias is withheld, never one of them — the
// host has no rule that makes one of two simultaneous claims the winner, and
// picking the first read would make what runs depend on the order a mount was
// walked in.
func routeAliasConflicts(
	appliedState []solutionhost.Applied, delivered map[string]int,
	candidates []SolutionHostBindingDecision,
) map[string]string {
	owners := map[string][]string{}
	isCandidate := map[string]bool{}
	for _, candidate := range candidates {
		isCandidate[candidate.Document.Binding] = true
		for _, alias := range candidate.Document.Aliases() {
			owners[alias] = append(owners[alias], candidate.Document.Binding)
		}
	}
	for _, record := range appliedState {
		if record.Removed || delivered[record.Binding] > 0 {
			continue
		}
		for _, alias := range record.Routes {
			owners[alias] = append(owners[alias], record.Binding)
		}
	}

	conflicted := map[string]string{}
	aliases := make([]string, 0, len(owners))
	for alias := range owners {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		claimants := owners[alias]
		if len(claimants) < 2 {
			continue
		}
		sort.Strings(claimants)
		for _, binding := range claimants {
			if !isCandidate[binding] {
				continue
			}
			// composition.ErrCollision is the class core itself answers a route
			// collision with, so a withheld binding's reason reads the same
			// whether core refused it or the host attributed it.
			conflicted[binding] = fmt.Sprintf(
				"%s: route %q is claimed by %v; aliases are unique within a host and the "+
					"collision is refused before the generation applies",
				composition.ErrCollision, alias, claimants)
		}
	}
	return conflicted
}

func appliedByBinding(records []solutionhost.Applied) map[string]solutionhost.Applied {
	byBinding := make(map[string]solutionhost.Applied, len(records))
	for _, record := range records {
		byBinding[record.Binding] = record
	}
	return byBinding
}

func documentsOf(candidates []SolutionHostBindingDecision) []*solutionhost.SolutionHostBinding {
	documents := make([]*solutionhost.SolutionHostBinding, 0, len(candidates))
	for _, candidate := range candidates {
		documents = append(documents, candidate.Document)
	}
	return documents
}
