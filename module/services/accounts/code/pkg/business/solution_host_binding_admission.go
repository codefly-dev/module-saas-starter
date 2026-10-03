package business

import (
	"github.com/codefly-dev/core/solutionhost"
)

// Admission of a delivered desired set (issue #952).
//
// core's solutionhost.Host.Admit is the judge and the whole judge. It answers
// one Admission per document — a decision, or the reason that document alone was
// refused — so a host reads the refusals it must expose straight off the result.
//
// This file used to narrow the set by hand: judge each document alone, then ask
// core about the survivors, then attribute the cross-document refusals it
// answered with one error. core v0.7.0 made that unnecessary. It attributes per
// document itself, and it refuses alias collisions one at a time until the set
// is stable — which is the subtlety that made the hand-rolled version wrong
// before its test caught it: refusing a document changes the question, because
// the generation it would have replaced keeps the aliases the set assumed it was
// releasing. Deleting the local version is the point. A host that re-derives its
// dependency's rules owns a second copy of them, and the copy is what drifts.
//
// core cd443989 narrowed the door further: Admit takes *solutionhost.Delivered,
// which only VerifyDelivered constructs from a signed carrier and this host's
// own BundleVerifier. So "this document was attested" is held by the compiler
// rather than by a naming convention, and there is no sequence of calls that
// reaches a judgement on bytes nobody verified.
//
// What remains here is the part that is this host's: reading core's answer into
// the shape the reconcile pass applies.

// SolutionHostBindingDecision is one admitted document and what the host should
// do with it.
type SolutionHostBindingDecision struct {
	// Delivered is the attested carrier this decision was made on. The apply
	// re-admits inside its own transaction, and that re-admission is a HOST
	// judgement, so it needs the verified value rather than the document read
	// out of it — handing core the document again would be the second copy of
	// "verified" that *Delivered exists to make impossible.
	Delivered *solutionhost.Delivered
	Document  *solutionhost.SolutionHostBinding
	Decision  solutionhost.Decision
}

// solutionHostBindingAdmission is the outcome of one pass's admission: the
// documents to reconcile, and the reason each withheld binding was withheld.
type solutionHostBindingAdmission struct {
	Accepted []SolutionHostBindingDecision
	Withheld map[string]string
}

// admitSolutionHostBindings asks core about the whole desired set and sorts its
// answer into what to apply and what to expose as refused.
//
// core's error is deliberately ignored: it is non-nil whenever ANY document was
// refused, which is the right answer for a caller that applies all or nothing — a
// renderer refusing bytes it is about to write. A host applies what is sound and
// exposes the rest, so it reads the Admissions. Nothing core refused is applied,
// because an Admission with an Err carries no decision.
//
// A document core refused before it could read a binding ID is attributed to the
// empty key and reported by the caller against the document it came from; it
// cannot collide with a real binding, because core refuses "" as a binding ID.
func admitSolutionHostBindings(
	host solutionhost.Host, delivered []*solutionhost.Delivered,
) solutionHostBindingAdmission {
	result := solutionHostBindingAdmission{Withheld: map[string]string{}}
	documents := make([]*solutionhost.SolutionHostBinding, len(delivered))
	for index, one := range delivered {
		documents[index] = one.Document()
	}
	admissions, err := host.Admit(delivered...)
	if admissions == nil {
		// core refused the call itself rather than any one document — applied
		// state handed to it without a coordinate, or an applied record it
		// could not read. Nothing is admitted, and the reason is attributed to
		// every delivered binding, because the host cannot tell which document
		// the refusal is about.
		for _, document := range documents {
			result.Withheld[document.Binding] = err.Error()
		}
		return result
	}
	result.Accepted = make([]SolutionHostBindingDecision, 0, len(admissions))
	for index, admission := range admissions {
		if admission.Err != nil {
			binding := admission.Binding
			if binding == "" && index < len(documents) {
				binding = documents[index].Binding
			}
			result.Withheld[binding] = admission.Err.Error()
			continue
		}
		result.Accepted = append(result.Accepted, SolutionHostBindingDecision{
			Delivered: delivered[index],
			Document:  documents[index],
			Decision:  admission.Decision,
		})
	}
	return result
}
