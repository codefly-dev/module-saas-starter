package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// WORK_CONTEXTS.md tells a platform administrator how to write a module
// principal's grant in the module envelope, and the grant is authorization: a
// reader who follows a sentence that stopped being true grants the wrong thing,
// or believes a change took effect that has not. The document once said the
// producer's permissions contribution names the resource ids in a grant (the
// contribution schema cannot express an id), and later that the registry was
// parsed once at start-up (it is now derived on every reconcile pass). Both
// sentences were load bearing for the advice built on them, which is how prose
// about a security mechanism drifts without a single test failing. These checks
// tie the claims that decide what an administrator writes to the artifacts that
// decide them.

const workContextsDoc = "WORK_CONTEXTS.md"

const (
	// moduleOperationAudienceSource validates an installed binding, resource ids
	// included, when the envelope is written and when an entry is derived.
	moduleOperationAudienceSource = "services/accounts/code/pkg/business/module_operation_audience.go"
	// accountsProcessWiring is the process wiring. It is where a registry read
	// from the environment used to live, and where one would come back.
	accountsProcessWiring = "services/accounts/code/work.go"
	// accountsCode holds every non-test source of the accounts service.
	accountsCode = "services/accounts/code"
	// modulePrincipalReconciler is the one production writer of the registry.
	modulePrincipalReconciler = "services/accounts/code/pkg/business/module_principal_reconcile.go"
)

// resourceIDBound extracts the length limit installed resource ids are held to.
// The document quotes it because a digest is always inside it and a
// hand-written identity need not be, so it is the one number a composition
// choosing an id by hand can exceed.
var resourceIDBound = regexp.MustCompile(`sortedUniqueOperationValues\(scope\.ResourceIDs,\s*(\d+)\)`)

// registryWriters are the setters that replace the registry every capability
// decision reads: the derived install, tagged with the envelope revision the
// fence compares (both its original and exported names), and the untagged one
// tests state a registry with.
var registryWriters = []string{"installDerivedModulePrincipals", "InstallDerivedModulePrincipals", "SetModulePrincipals"}

// writesRegistry reports a *call* to a setter. Counting calls against
// declarations rather than matching a call's shape is deliberate: a qualified
// call (`service.Set...`) and a receiver call inside the declaring package read
// differently, and a check that only recognised one form would miss the very
// change it exists to catch.
func writesRegistry(body string) bool {
	for _, writer := range registryWriters {
		if strings.Count(body, writer+"(") > strings.Count(body, ") "+writer+"(") {
			return true
		}
	}
	return false
}

// readsModulePrincipalsVariable reports source that names the retired
// environment variable outside a comment: a string literal is the only way a
// process reads it.
var readsModulePrincipalsVariable = regexp.MustCompile("[\"`]MODULE_PRINCIPALS[\"`]")

// TestWorkContextsContractNamesTheEnforcedResourceIDBound holds the quoted
// bound to the enforced one. Exceeding it is refused when the envelope is
// written, so an administrator who trusted a stale number in the document
// learns the real one from a rejected write rather than from a denial.
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

// TestWorkContextsContractDerivesTheRegistryFromDeliveredAuthority pins the
// fact the document's advice now rests on: the module principal registry is
// derived each reconcile pass from delivered authority inside the module
// envelope, and nothing else writes it. Two ways that would silently stop being
// true, each refused here:
//
//   - a process reading the registry from its environment again — the
//     MODULE_PRINCIPALS read this replaced, under which an entry change was a
//     restart and an entry nobody delivered could still grant;
//   - a second production writer of the registry, which would be a second
//     answer to "which modules may act here" beside the derivation.
func TestWorkContextsContractDerivesTheRegistryFromDeliveredAuthority(t *testing.T) {
	root := filepath.Join(findModuleDir(t), filepath.FromSlash(accountsCode))
	writers := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "gen" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, _ := filepath.Rel(findModuleDir(t), path)
		relative = filepath.ToSlash(relative)
		if readsModulePrincipalsVariable.Match(body) {
			t.Errorf("%s names MODULE_PRINCIPALS: %s says the registry is derived from delivered authority, and no process reads it from its environment",
				relative, workContextsDoc)
		}
		if writesRegistry(string(body)) {
			writers[relative] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", accountsCode, err)
	}
	if len(writers) != 1 || !writers[modulePrincipalReconciler] {
		t.Errorf("the production writers of the module registry are %v; %s says the reconciler in %s is the only one",
			writers, workContextsDoc, modulePrincipalReconciler)
	}
	if strings.Contains(readModuleFile(t, accountsProcessWiring), "ParseModulePrincipalRegistry") {
		t.Errorf("%s parses a module principal registry at start-up", accountsProcessWiring)
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
		"The module principal registry is derived, each reconcile pass, from the delivered authority documents inside the module envelope",
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
