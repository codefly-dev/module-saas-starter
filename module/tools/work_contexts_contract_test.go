package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// WORK_CONTEXTS.md tells a composition how to write a MODULE_PRINCIPALS entry,
// and the entry is authorization: a reader who follows a sentence that stopped
// being true grants the wrong thing, or believes a change took effect that has
// not. The document previously said the host re-reads that entry on every call
// and that the producer's permissions contribution names the resource ids in
// it. Neither is true — the registry is parsed once at start-up, and the
// contribution schema cannot express an id — and both sentences were load
// bearing for the advice built on them, which is how prose about a security
// mechanism drifts without a single test failing. These checks tie the claims
// that decide what a composition writes to the artifacts that decide them.

const workContextsDoc = "WORK_CONTEXTS.md"

const (
	// moduleOperationAudienceSource validates an installed binding, resource ids
	// included, when the registry is parsed.
	moduleOperationAudienceSource = "services/accounts/code/pkg/business/module_operation_audience.go"
	// modulePrincipalRegistrySource is the process wiring that parses
	// MODULE_PRINCIPALS. It is the whole reason the entry is start-up
	// configuration rather than per-request state.
	modulePrincipalRegistrySource = "services/accounts/code/work.go"
	// accountsRequestPath holds every package a request can reach. A parse of the
	// registry appearing in here would make the entry per-call after all.
	accountsRequestPath = "services/accounts/code/pkg"
)

// resourceIDBound extracts the length limit installed resource ids are held to.
// The document quotes it because a digest is always inside it and a
// hand-written identity need not be, so it is the one number a composition
// choosing an id by hand can exceed.
var resourceIDBound = regexp.MustCompile(`sortedUniqueOperationValues\(scope\.ResourceIDs,\s*(\d+)\)`)

// registryParser is the one function that turns MODULE_PRINCIPALS into the
// registry a request reads.
const registryParser = "ParseModulePrincipalRegistry"

// parsesRegistry reports a *call* to the parser. Counting calls against
// declarations rather than matching a call's shape is deliberate: a qualified
// call (`business.Parse...`) and a bare one inside the declaring package read
// differently, and a check that only recognised the qualified form would miss
// the very change it exists to catch.
func parsesRegistry(body string) bool {
	return strings.Count(body, registryParser+"(") > strings.Count(body, "func "+registryParser+"(")
}

// TestWorkContextsContractNamesTheEnforcedResourceIDBound holds the quoted
// bound to the enforced one. Exceeding it is refused while the registry is
// parsed, which fails accounts' start-up rather than one call, so a composition
// that trusted a stale number in the document does not learn the real one from
// a denial — it learns it from a service that will not boot.
func TestWorkContextsContractNamesTheEnforcedResourceIDBound(t *testing.T) {
	match := resourceIDBound.FindStringSubmatch(readModuleFile(t, moduleOperationAudienceSource))
	if match == nil {
		t.Fatalf("no resource id bound in %s", moduleOperationAudienceSource)
	}
	if !strings.Contains(collapsed(readModuleFile(t, workContextsDoc)), "at most "+match[1]+" characters") {
		t.Errorf(
			"%s does not state the %s-character resource id bound %s enforces",
			workContextsDoc, match[1], moduleOperationAudienceSource,
		)
	}
}

// TestWorkContextsContractKeepsTheRegistryAtProcessStart pins the fact the
// document's advice rests on: MODULE_PRINCIPALS is parsed once, by the process
// wiring, so re-issuing an entry is a deployment and not an edit. Were a
// request path to parse it instead, the entry would be editable in place and
// the document's whole argument for naming a stable identity — that a re-issue
// costs a rollout — would be wrong in the direction that reads as safe.
func TestWorkContextsContractKeepsTheRegistryAtProcessStart(t *testing.T) {
	if !parsesRegistry(readModuleFile(t, modulePrincipalRegistrySource)) {
		t.Fatalf(
			"%s no longer parses MODULE_PRINCIPALS; %s names it as the start-up read",
			modulePrincipalRegistrySource, workContextsDoc,
		)
	}
	root := filepath.Join(findModuleDir(t), filepath.FromSlash(accountsRequestPath))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if parsesRegistry(string(body)) {
			relative, _ := filepath.Rel(findModuleDir(t), path)
			t.Errorf(
				"%s parses MODULE_PRINCIPALS on a request path: %s says the entry is read once at start-up",
				filepath.ToSlash(relative), workContextsDoc,
			)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", accountsRequestPath, err)
	}
}

// TestWorkContextsContractStatesTheGrantNamingDecision keeps the conclusions in
// the file. Each one was absent or stated backwards while the document taught a
// digest-shaped grant, and each changes what a composition writes: who owns an
// id's meaning, what the host will and will not check for it, which contexts an
// entry change actually revokes, and where a definition gets pinned instead.
func TestWorkContextsContractStatesTheGrantNamingDecision(t *testing.T) {
	document := collapsed(readModuleFile(t, workContextsDoc))
	// One pin per distinct conclusion; a paragraph that merely restates one is
	// not pinned, so an honest rewrite does not break the build.
	for _, claim := range []string{
		"the producing module owns what its ids mean",
		"The host neither derives nor checks them",
		"Name the resource by its **stable identity**, not by a digest of its current definition.",
		"`MODULE_PRINCIPALS` is parsed once, at accounts start-up",
		"an entry change does not revoke them",
		"Pinning a definition is the producer's own check",
		"The host makes no such check",
	} {
		if !strings.Contains(document, collapsed(claim)) {
			t.Errorf("%s no longer states %q", workContextsDoc, claim)
		}
	}
}

// TestWorkContextsContractResolvesItsRevisionCrossReference keeps the whole-entry
// revision in one place. The naming advice cites that section rather than
// restating its rules, because the restatement it replaced had already drifted
// from them — it dropped the qualifier that the change lands with the
// deployment. A dead link puts the mechanism nowhere at all.
func TestWorkContextsContractResolvesItsRevisionCrossReference(t *testing.T) {
	document := readModuleFile(t, workContextsDoc)
	const heading = "## Operation contexts with no person present"
	const anchor = "](#operation-contexts-with-no-person-present)"
	if !strings.Contains(collapsed(document), anchor) {
		t.Fatalf("%s no longer cites the whole-entry revision section", workContextsDoc)
	}
	if !strings.Contains(document, heading) {
		t.Errorf("%s cites %q, but has no %q heading", workContextsDoc, anchor, heading)
	}
}
