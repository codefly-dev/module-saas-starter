package business

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The narrowing gate. It reads this package's own source, derives every
// *Service method that performs a narrowing of authority, and requires each one
// to go through WithPolicyLoggedNarrowing — or to be classified, by name, with a
// reason a reader can argue with and a PREMISE this test asserts.
//
// WHY IT DERIVES THE SET RATHER THAN PINNING IT. A gate that counted wrapped
// call sites against a number would be worthless here, and not subtly: two
// branches that each add one narrowing and each bump the same number merge
// cleanly into a total that is silently wrong, and nothing in the merge looks
// like a conflict. The same is true of a pinned list of method names — the list
// is the thing a person forgets to touch. So the inventory comes out of the AST
// on every run: a method added tomorrow is in it the moment it is written, and
// a method that is neither wrapped nor classified fails this test by name.
//
// THE THREE CLASSIFICATIONS, and why there are three rather than two:
//
//   - WITNESSED: the method's body calls WithPolicyLoggedNarrowing. Nothing to
//     declare.
//   - narrowsNoAuthority: the method's verb is a narrowing verb but what it
//     narrows is not authority — a dashboard, a notification, a sign-in path,
//     the caller's own second factor. Each entry carries the reason and a
//     premise this test checks.
//   - unwitnessedNarrowings: the method DOES narrow authority and is NOT
//     witnessed. A declared debt, not a waiver. Pretending these are exempt
//     would be the lie this gate exists to prevent, and leaving them
//     unclassified would make the gate red for everyone and get it deleted. So
//     each one is named, with what it narrows and why it is not witnessed yet,
//     and the premise asserted is that it is still unwitnessed — the day
//     somebody wires it, this test fails until the entry is deleted.
//
// WHAT THE PREMISES CAN AND CANNOT DO. A premise is a TRIPWIRE, not a proof: it
// fails when a classified method starts doing something its reason says it does
// not do. It cannot establish that the reason was right in the first place —
// that is a judgement, which is why every entry is a sentence rather than a
// name. Two further limits, named rather than implied away: the inventory is
// keyed on exported *Service methods whose name begins with a narrowing verb, so
// a narrowing added as an unexported helper of a method named something else is
// outside its reach, and so is one whose method name begins with a verb nobody
// thought of. Both are failures of the naming convention, and the convention is
// what the gate reads.

// narrowingVerbs are the method-name prefixes that make a method a candidate.
// A verb added here can only ever widen the gate's reach.
var narrowingVerbs = []string{"Revoke", "Remove", "Delete", "Uninstall", "Close", "Disable"}

// premise is what this test checks about a classified method, so the
// classification cannot quietly stop being true.
type premise int

const (
	// premiseNoAuthorityWrite: the method's body makes no call that both names
	// an authority relation and writes to it. It fails the day the method starts
	// revoking a grant, a membership, a principal or a credential.
	premiseNoAuthorityWrite premise = iota
	// premiseOwnUserOnly: the method writes only inside a user-scoped
	// transaction, so the only authority it can reach is the acting user's own.
	// It fails the day the write moves to a tenant or control-plane transaction,
	// where it could reach somebody else's.
	premiseOwnUserOnly
	// premiseStillUnwitnessed: the method does not call
	// WithPolicyLoggedNarrowing. The premise of a DEBT entry, so the entry
	// cannot outlive the debt.
	premiseStillUnwitnessed
)

// classification is one declared answer about one method.
type classification struct {
	why     string
	premise premise
	// absentSubjectKind, when set, is a PolicyLogSubjectKind value whose
	// ABSENCE from the closed set is part of this entry's reason. Adding that
	// kind fails the entry, which is the point: the reason was "the log cannot
	// name this subject", and once it can, the entry has to be re-argued.
	absentSubjectKind PolicyLogSubjectKind
}

// narrowsNoAuthority are the methods whose narrowing verb is about something
// other than authority. Each reason is a claim about what the method does, and
// the premise beside it is what this test checks so the claim cannot rot.
var narrowsNoAuthority = map[string]classification{
	"DeleteDashboard": {
		why:     "a dashboard is a saved view. Deleting one removes content; nobody's grant, membership or credential changes, and no reader loses access to anything but the view itself.",
		premise: premiseNoAuthorityWrite,
	},
	"DeleteNotification": {
		why:     "a notification is a message to one person, deleted by that person. It confers nothing, so its removal reduces nobody's authority.",
		premise: premiseOwnUserOnly,
	},
	"RevokeMFADevice": {
		why:     "the caller removes their OWN second factor. It narrows how that person may authenticate, not what anyone may do once authenticated, and the person doing it is the person it affects.",
		premise: premiseOwnUserOnly,
	},
	"DeleteSubscription": {
		why:     "a webhook subscription is a delivery destination. Deleting one stops events being sent to an endpoint; it grants and revokes nothing, and the endpoint never held authority here.",
		premise: premiseNoAuthorityWrite,
	},
	"DeleteSourceCredential": {
		why:     "the credential is this host's own means of reaching a third-party source. Deleting it narrows what THIS host can do outward, not what any principal may do here.",
		premise: premiseNoAuthorityWrite,
	},
	"DeleteDatasourceAccountLink": {
		why:     "an account link records that a person's third-party account is theirs. It is evidence, not authority: the membership read in this method is the authorisation for the removal, never its subject.",
		premise: premiseNoAuthorityWrite,
	},
	"DeleteDatasourceDomain": {
		why:     "a claimed domain is a verified fact about an organisation. Removing the claim grants nothing and revokes nothing a principal holds.",
		premise: premiseNoAuthorityWrite,
	},
	"DisableOrgIdentityProvider": {
		why:     "disabling a provider closes a SIGN-IN path and nothing else — the method's own documentation says sessions already minted stay valid until they expire. Nobody loses a role, a membership or a scope.",
		premise: premiseNoAuthorityWrite,
	},
	"DisableSSO": {
		why:     "the same as disabling an identity provider: the organisation stops authenticating that way, while every grant inside it stands untouched.",
		premise: premiseNoAuthorityWrite,
	},
	"RevokeInvitation": {
		why:     "an invitation is an OFFER of authority that has not been taken. Until it is accepted the invitee holds nothing, so revoking it reduces nothing — and accepting one is the grant, which is a widening the log records on its own path.",
		premise: premiseNoAuthorityWrite,
	},
}

// unwitnessedNarrowings are narrowings of authority that do NOT go through the
// policy log. Each is a known hole, named with what it narrows and why it is
// still open — not a waiver of the requirement, and not a place to park a new
// one: a method added here needs a sentence somebody has to write and a reader
// can refuse.
var unwitnessedNarrowings = map[string]classification{
	"DeleteRole": {
		why:     "deleting a custom role narrows every subject that holds it, all at once. Witnessing it needs the entry to name a subject the closed set has no kind for — the ROLE, not an assignment of it — so it waits on that kind the same way the organisation archive does.",
		premise: premiseStillUnwitnessed,
	},
	"DeleteTeam": {
		why:     "a team is a subject roles are granted to, so deleting one narrows every member through it — the same fact RemoveTeamMember witnesses one member at a time. It needs one entry for the team, which is a subject the closed set does not name either.",
		premise: premiseStillUnwitnessed,
	},
	"DisableAgentPrincipal": {
		why:     "a reversible suspension of an agent is a narrowing while it stands, and lifting it is the authorised regrant the log exists to distinguish from 'never taken away'. Both halves belong in the log, and the pair is a decision about regrants rather than a second copy of the revoke path.",
		premise: premiseStillUnwitnessed,
	},
	"DeleteUser": {
		why:     "soft-deleting an identity makes it unusable everywhere at once, which is the broadest narrowing this host performs. It is not witnessed because the subject is a user rather than a principal, and the two are deliberately separate lifecycles here.",
		premise: premiseStillUnwitnessed,
	},
	"DeleteDatasourceSource": {
		why:     "removing a source revokes the delegations that named it, in the same transaction. The delegation revocations are the narrowing, and witnessing them needs one entry for the source — the fact they all share — rather than the per-delegation entries this path would otherwise append.",
		premise: premiseStillUnwitnessed,
	},
	"RevokeSession": {
		why:               "ending a session ends live minted authority, so it is a narrowing. The closed subject-kind set names no session, and a session is the one subject whose authority expires on its own — which is the argument for a kind of its own rather than for borrowing another.",
		premise:           premiseStillUnwitnessed,
		absentSubjectKind: "session",
	},
}

// narrowingMethod is one candidate the AST walk found.
type narrowingMethod struct {
	name      string
	file      string
	line      int
	witnessed bool
	// authorityWrites are calls in the body that both name an authority
	// relation and write to it, as the premise check reads them.
	authorityWrites []string
	// transactions are the transaction openers the body uses.
	transactions []string
}

func TestPolicyLogNarrowings_EveryNarrowingPathIsWitnessed(t *testing.T) {
	methods := collectNarrowingMethods(t)
	require.NotEmpty(t, methods, "the gate found no narrowing methods at all; the AST walk is broken")
	require.True(t, len(methods) > len(narrowsNoAuthority)+len(unwitnessedNarrowings),
		"every candidate the walk found is classified away and none is witnessed; that cannot be right, so the walk or the classifications are wrong")

	usedExempt := map[string]bool{}
	usedDebt := map[string]bool{}
	var violations []string

	for _, method := range methods {
		exempt, isExempt := narrowsNoAuthority[method.name]
		debt, isDebt := unwitnessedNarrowings[method.name]

		require.False(t, isExempt && isDebt,
			"%s is classified both as narrowing no authority and as an unwitnessed narrowing; it cannot be both", method.name)

		switch {
		case method.witnessed:
			if isExempt {
				violations = append(violations, fmt.Sprintf(
					"%s:%d %s goes through WithPolicyLoggedNarrowing but is listed in narrowsNoAuthority; delete the entry",
					method.file, method.line, method.name))
			}
			if isDebt {
				violations = append(violations, fmt.Sprintf(
					"%s:%d %s is witnessed now — delete its unwitnessedNarrowings entry, the debt is paid",
					method.file, method.line, method.name))
			}
		case isExempt:
			usedExempt[method.name] = true
			_ = exempt
		case isDebt:
			usedDebt[method.name] = true
			_ = debt
		default:
			violations = append(violations, fmt.Sprintf(
				"%s:%d %s narrows authority by its name and does not go through WithPolicyLoggedNarrowing. "+
					"Wrap it with an entry builder in policy_log_narrowings.go — or, if it narrows something other "+
					"than authority, add it to narrowsNoAuthority with the sentence that says why; if it does narrow "+
					"authority and cannot be witnessed yet, declare it in unwitnessedNarrowings with what it narrows "+
					"and what is blocking.",
				method.file, method.line, method.name))
		}
	}

	sort.Strings(violations)
	require.Empty(t, violations, "narrowing coverage violations:\n%s", strings.Join(violations, "\n"))

	for name := range narrowsNoAuthority {
		require.True(t, usedExempt[name],
			"stale narrowsNoAuthority entry %q: no such unwitnessed narrowing-verb method any more — delete the entry", name)
	}
	for name := range unwitnessedNarrowings {
		require.True(t, usedDebt[name],
			"stale unwitnessedNarrowings entry %q: no such unwitnessed narrowing-verb method any more — delete the entry", name)
	}
}

// TestPolicyLogNarrowings_ClassificationPremisesHold is the half that makes the
// classifications worth having. Each entry says what its method does; this
// asserts the thing that would have to change for the sentence to be false.
func TestPolicyLogNarrowings_ClassificationPremisesHold(t *testing.T) {
	byName := map[string]narrowingMethod{}
	for _, method := range collectNarrowingMethods(t) {
		byName[method.name] = method
	}

	kinds := map[PolicyLogSubjectKind]bool{}
	for _, kind := range []PolicyLogSubjectKind{
		PolicyLogPrincipal, PolicyLogBinding, PolicyLogInstallation,
		PolicyLogTeamMembership, PolicyLogScopeGrant,
	} {
		kinds[kind] = true
	}

	check := func(t *testing.T, name string, declared classification) {
		t.Helper()
		method, found := byName[name]
		require.True(t, found, "%s is classified but no longer exists; the stale-entry check in the test above covers this", name)

		switch declared.premise {
		case premiseNoAuthorityWrite:
			require.Empty(t, method.authorityWrites,
				"%s is classified as narrowing no authority (%s) but its body writes to an authority relation: %s. "+
					"Either the classification is wrong and this is a narrowing to witness, or the call is a read "+
					"the premise check cannot tell from a write.",
				name, declared.why, strings.Join(method.authorityWrites, ", "))
		case premiseOwnUserOnly:
			require.NotEmpty(t, method.transactions,
				"%s is classified as reaching only the acting user's own rows, but opens no transaction at all; the premise cannot be checked", name)
			for _, opener := range method.transactions {
				require.Equal(t, "WithUserTx", opener,
					"%s is classified as reaching only the acting user's own rows (%s) but writes inside %s, which can reach somebody else's",
					name, declared.why, opener)
			}
		case premiseStillUnwitnessed:
			require.False(t, method.witnessed,
				"%s is declared an unwitnessed narrowing but is witnessed now; delete its entry", name)
		}

		if declared.absentSubjectKind != "" {
			require.False(t, kinds[declared.absentSubjectKind],
				"%s is classified on the grounds that the closed subject-kind set cannot name a %q, and now it can. "+
					"Re-argue the entry: the reason it rested on is gone.",
				name, declared.absentSubjectKind)
			require.False(t, declared.absentSubjectKind.Valid(),
				"%q is accepted by PolicyLogSubjectKind.Valid but is not in the set this test enumerates; one of the two is out of date",
				declared.absentSubjectKind)
		}
	}

	for name, declared := range narrowsNoAuthority {
		t.Run("narrows_no_authority/"+name, func(t *testing.T) { check(t, name, declared) })
	}
	for name, declared := range unwitnessedNarrowings {
		t.Run("unwitnessed/"+name, func(t *testing.T) { check(t, name, declared) })
	}
}

// authorityRelations are the words that make a call's target an authority
// relation. A method that WRITES to one of these is narrowing or widening what
// somebody may do, whatever its own name says.
var authorityRelations = []string{
	"Role", "Permission", "Grant", "Share", "Scope", "Member", "Membership",
	"Principal", "APIKey", "Delegation", "Installation", "Team", "Session",
	"PlatformAdmin",
}

// readPrefixes are the call-name prefixes that make a call a read. Everything
// else that names an authority relation is treated as a write, which is the
// conservative direction: a misread read shows up as a classification to
// justify, a misread write shows up as nothing at all.
var readPrefixes = []string{
	"Get", "List", "Count", "Exists", "Resolve", "Lookup", "Load", "Read",
	"Find", "Is", "Has", "Can", "Require", "Verify", "Check", "Declared",
}

// transactionOpeners are the calls that open a store transaction, so a premise
// about which rows a method can reach has something to read.
var transactionOpeners = []string{"WithOrgTx", "WithUserTx", "WithControlPlane", "Within"}

// collectNarrowingMethods parses the package's own non-test sources and returns
// every exported *Service method whose name begins with a narrowing verb, with
// what the gate needs to know about its body.
func collectNarrowingMethods(t *testing.T) []narrowingMethod {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var out []narrowingMethod
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err, "parse %s", name)

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			if !isServiceReceiver(fn.Recv) || !ast.IsExported(fn.Name.Name) || !hasNarrowingVerb(fn.Name.Name) {
				continue
			}
			method := narrowingMethod{
				name: fn.Name.Name,
				file: name,
				line: fset.Position(fn.Pos()).Line,
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				called := calledName(call)
				if called == "" {
					return true
				}
				switch {
				case called == "WithPolicyLoggedNarrowing":
					method.witnessed = true
				case contains(transactionOpeners, called):
					method.transactions = append(method.transactions, called)
				case namesAuthorityRelation(called) && !hasReadPrefix(called) && !isValueProjection(called):
					method.authorityWrites = append(method.authorityWrites, called)
				}
				return true
			})
			out = append(out, method)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// isServiceReceiver reports whether a method is on *Service, whatever the
// receiver is named. Keying on a receiver named "s" would let a method that
// names its receiver anything else narrow authority unseen.
func isServiceReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) != 1 {
		return false
	}
	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Service"
}

func hasNarrowingVerb(name string) bool {
	for _, verb := range narrowingVerbs {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

// calledName is the name a call expression invokes — the selector for a method
// call, the identifier for a plain function call.
func calledName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

func namesAuthorityRelation(called string) bool {
	for _, relation := range authorityRelations {
		if strings.Contains(called, relation) {
			return true
		}
	}
	return false
}

// isValueProjection reports whether a call is a pure conversion of a value
// already in hand — an enum to the spelling a column uses, a row to its wire
// form. It names an authority relation and touches none: there is no relation
// on the other side of it to write to.
func isValueProjection(called string) bool {
	return strings.Contains(called, "ToString") ||
		strings.Contains(called, "ToProto") ||
		strings.Contains(called, "Payload") ||
		strings.Contains(called, "Labels")
}

func hasReadPrefix(called string) bool {
	for _, prefix := range readPrefixes {
		if strings.HasPrefix(called, prefix) || strings.HasPrefix(called, strings.ToLower(prefix[:1])+prefix[1:]) {
			return true
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}
