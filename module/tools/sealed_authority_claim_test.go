package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `accounts/AGENTS.md` described the `min(sealed, live)` model in the PRESENT
// TENSE for as long as none of it existed — sealed ceilings, installation
// revisions, producer epochs, build incarnations, and verification by exact
// binding lookup. An adversarial review caught it, not a gate.
//
// That is a worse failure than an out-of-date document. `AGENTS.md` is the file
// other agents read to decide what is already enforced, so a false present tense
// there does not merely mislead a human: it tells the next agent that a check it
// is about to rely on is in place.
//
// Nothing could have caught it, because a prose claim has no referent. This test
// gives it one, in the only direction that is checkable: while the code still
// performs the lookup the model FORBIDS, the document must carry the marker
// saying the model is not built. The two then move together — the day somebody
// replaces the search with an exact lookup, this test fails and names the
// paragraph to update, instead of the paragraph quietly becoming true-by-accident
// or (as happened) staying false for a release.
//
// It deliberately anchors on a symbol rather than on a count or a line number.
// `operationScopesSubset` IS the forbidden behaviour: searching a principal's
// bindings for one whose scopes happen to contain the presented set, rather than
// resolving the one binding the credential sealed.
func TestSealedAuthorityClaimMatchesWhatTheCodeEnforces(t *testing.T) {
	moduleDir := findModuleDir(t)

	source, err := os.ReadFile(filepath.Join(moduleDir,
		"services/accounts/code/pkg/business/module_operation_context.go"))
	if err != nil {
		t.Fatalf("read module_operation_context.go: %v", err)
	}
	// The scope SEARCH, which the sealed model replaces with an exact binding
	// lookup. Its presence is the fact this test reads.
	searchesForAScopeSuperset := strings.Contains(string(source), "operationScopesSubset(")

	document, err := os.ReadFile(filepath.Join(moduleDir, "services/accounts/AGENTS.md"))
	if err != nil {
		t.Fatalf("read accounts/AGENTS.md: %v", err)
	}
	// Whitespace collapsed: the claim is the sentence, not the column it wraps at.
	prose := strings.Join(strings.Fields(string(document)), " ")

	const (
		model = "min(sealed, live)"
		// The disclosure the searching call site requires.
		//
		// This replaced a blunt "NOT BUILT YET" marker over the whole claim. That
		// marker was right while nothing was built, and became WRONG when the
		// live half landed: it would have forced the document to disclaim an
		// enforcement that now exists, and a reader told the whole thing is
		// absent stops checking which part is.
		//
		// So the gate now keys on the specific site. The live re-read is built;
		// moduleOperationContextStale still searches; the document must say so in
		// those words while it does.
		stillSearches = "still searches"
		searchSite    = "moduleOperationContextStale"
		exactOnly     = "exact binding lookup"
	)

	if !strings.Contains(prose, model) {
		t.Fatalf("accounts/AGENTS.md no longer mentions %q.\n"+
			"It is the authority model the whole module-capability surface is held to; "+
			"deleting the paragraph is not how it stops being wrong.", model)
	}

	if searchesForAScopeSuperset {
		for _, required := range []string{stillSearches, searchSite} {
			if strings.Contains(prose, required) {
				continue
			}
			t.Fatalf("accounts/AGENTS.md describes %q but is missing the phrase %q, which the disclosure about %s needs, "+
				"while module_operation_context.go still calls operationScopesSubset.\n\n"+
				"That call site iterates a principal's bindings and accepts if ANY of them "+
				"contains the presented scopes, so a context minted against a narrow binding "+
				"is satisfied by any wider binding the principal also holds — narrowing one "+
				"achieves nothing while a broader one survives.\n\n"+
				"The live re-read IS built, so a blanket disclaimer over the whole model would "+
				"now be wrong in the other direction. Name this site and what it still does, "+
				"or route it through ExactOperationBinding.",
				model, required, searchSite)
		}
		// The requirement must survive the disclosure, so naming a gap cannot be
		// used to quietly drop the rule it defers.
		if !strings.Contains(prose, exactOnly) {
			t.Fatalf("accounts/AGENTS.md discloses that %s still searches but no longer states the "+
				"requirement that replaces it (%q).\n"+
				"Naming a gap is not the same as dropping the rule: whoever builds this "+
				"has to be able to read what it must do.", searchSite, exactOnly)
		}
		return
	}

	// The search is gone. If the document still says the model is unbuilt, it is
	// now wrong in the OTHER direction — understating an enforcement, which makes
	// the next agent add a redundant check or distrust a real one.
	if strings.Contains(prose, stillSearches) && strings.Contains(prose, searchSite) {
		t.Fatalf("module_operation_context.go no longer calls operationScopesSubset, "+
			"but accounts/AGENTS.md still says %q about %s.\n\n"+
			"The forbidden scope search is gone, so the paragraph needs re-reading against "+
			"what is now enforced: if the sealed ceiling and the exact binding lookup are in "+
			"place, state the model in the present tense and delete the gap; if only part of "+
			"it landed, say which part.", model, searchSite)
	}
}
