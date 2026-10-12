//go:build !race_only

package business_test

// The declaration this host contributes, held to two things it cannot check at
// runtime: the exact wire shape an executing module validates, and the absence
// of any clock in this host that would fire the same schedule a second time.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// triggerContractFields are the field names an declared release's trigger
// declaration carries, as the module that validates it spells them.
//
// They are PINNED here, by name, because this host cannot verify them: the
// declaration leaves as JSON a composition folds into an installation
// document, and a rename on either side is a release that will not install
// rather than a schedule that visibly breaks. A test that only checked this
// host's own round-trip would pass through any rename at all.
var triggerContractFields = struct {
	schedule []string
	identity []string
}{
	schedule: []string{"cron", "timezone", "missed_run", "overlap", "deadline_millis", "input"},
	identity: []string{"source_delegation", "installation_binding"},
}

// The declared schedule and identity serialize to exactly the contract's field
// names — no more, no fewer, and none renamed.
func TestTriggerDeclarationMatchesTheContractFieldNames(t *testing.T) {
	schedule, err := json.Marshal(business.DeclaredTriggerSchedule{
		Cron: "*/30 * * * *", Timezone: "UTC",
		MissedRun: business.DatasourceTriggerMissedRun, Overlap: business.DatasourceTriggerOverlap,
		DeadlineMillis: 1, Input: []byte(`{}`),
	})
	require.NoError(t, err)
	var scheduleFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schedule, &scheduleFields))
	require.ElementsMatch(t, triggerContractFields.schedule, keysOf(scheduleFields),
		"the schedule a release declares is validated by the module that fires it, field by field")

	// Both arms are optional on the wire and exactly one is ever set, so each
	// is serialized on its own: a shape carrying both would be a declaration
	// with two answers, and one carrying neither is refused at install.
	for _, identity := range []business.DeclaredTriggerIdentity{
		{SourceDelegation: "source"},
		{InstallationBinding: "binding"},
	} {
		encoded, err := json.Marshal(identity)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &fields))
		require.Len(t, fields, 1, "exactly one arm is named")
		require.Subset(t, triggerContractFields.identity, keysOf(fields))
		require.True(t, identity.Declared())
	}
	require.False(t, business.DeclaredTriggerIdentity{}.Declared(), "a trigger naming no identity is refused at install")
	require.False(t, business.DeclaredTriggerIdentity{SourceDelegation: "s", InstallationBinding: "b"}.Declared(),
		"naming both arms is a declaration with two answers")
	require.True(t, business.DeclaredTriggerIdentity{SourceDelegation: "s"}.Reference())
	require.False(t, business.DeclaredTriggerIdentity{InstallationBinding: "b"}.Reference())
}

func keysOf(fields map[string]json.RawMessage) []string {
	out := make([]string, 0, len(fields))
	for key := range fields {
		out = append(out, key)
	}
	return out
}

// The declared policies are values the contract enumerates. A policy spelled
// any other way is refused by the module that validates it, and a host that
// declared one would publish a release nobody can install.
func TestTriggerDeclarationUsesDeclaredPolicyValues(t *testing.T) {
	require.Contains(t, []string{"MISSED_RUN_POLICY_SKIP", "MISSED_RUN_POLICY_CATCH_UP"}, business.DatasourceTriggerMissedRun)
	require.Contains(t,
		[]string{"OVERLAP_POLICY_SKIP", "OVERLAP_POLICY_QUEUE", "OVERLAP_POLICY_CANCEL_PREVIOUS"},
		business.DatasourceTriggerOverlap)
	require.Equal(t, "UTC", business.DatasourceTriggerTimezone)
}

// The interval is spelled as a cron only when a five-field expression can name
// it, and refused by name otherwise. An interval rounded to something the
// grammar happens to accept is a schedule nobody asked for.
func TestTriggerScheduleSpellsOnlyTheIntervalsCronCanName(t *testing.T) {
	source := func(interval time.Duration) *business.DatasourceSource {
		next := time.Now().Add(interval)
		return &business.DatasourceSource{
			ID: "2a3f5c1e-0000-4000-8000-000000000001", OrgID: "org",
			Status: "active", ReconcileInterval: interval, NextReconcileAt: &next,
		}
	}
	for interval, want := range map[time.Duration]string{
		30 * time.Minute: "*/30 * * * *",
		15 * time.Minute: "*/15 * * * *",
		time.Minute:      "*/1 * * * *",
	} {
		declared := business.DeclareSourceTrigger(source(interval), nil, nil)
		require.True(t, declared.Declarable(), "%s: %s", interval, declared.UndeclarableReason)
		require.Equal(t, want, declared.Schedule.Cron, "interval %s", interval)
	}
	// A day, and a divisor of a day, are expressible; the minute (and hour) are
	// derived from the source so every source of one provider does not come due
	// in the same second.
	daily := business.DeclareSourceTrigger(source(24*time.Hour), nil, nil)
	require.True(t, daily.Declarable())
	require.Regexp(t, `^\d+ \d+ \* \* \*$`, daily.Schedule.Cron)
	twelveHourly := business.DeclareSourceTrigger(source(12*time.Hour), nil, nil)
	require.True(t, twelveHourly.Declarable())
	require.Regexp(t, `^\d+ \*/12 \* \* \*$`, twelveHourly.Schedule.Cron)

	// And the refusals, each by name rather than by approximation.
	for _, interval := range []time.Duration{
		25 * time.Minute, // restarts at the top of the hour: 25, 50, then 0
		90 * time.Second, // not a whole number of minutes
		5 * time.Hour,    // does not divide a day
		48 * time.Hour,   // beyond a day, which cron names by date, not by period
	} {
		declared := business.DeclareSourceTrigger(source(interval), nil, nil)
		require.False(t, declared.Declarable(), "interval %s was spelled %q", interval, declared.Schedule.Cron)
		require.Equal(t, business.TriggerUndeclarableInterval, declared.UndeclarableReason, "interval %s", interval)
	}
	disabled := business.DeclareSourceTrigger(&business.DatasourceSource{ID: "s", OrgID: "org", Status: "active"}, nil, nil)
	require.Equal(t, business.TriggerUndeclarableNoSchedule, disabled.UndeclarableReason)
}

// The declaration names the reference arm whenever a live reference exists,
// and the digest moves when the interval does — which is what makes an
// interval edit a re-declaration rather than the same release with different
// contents.
func TestTriggerDeclarationPrefersTheReferenceArmAndFingerprintsItself(t *testing.T) {
	next := time.Now().Add(time.Hour)
	source := &business.DatasourceSource{
		ID: "2a3f5c1e-0000-4000-8000-000000000002", OrgID: "org",
		Status: "active", ReconcileInterval: 30 * time.Minute, NextReconcileAt: &next,
	}
	live := []business.TriggerDelegationReference{{Reference: source.ID, DelegationID: "d1", Active: true}}
	revoked := []business.TriggerDelegationReference{{Reference: source.ID, DelegationID: "d1", Active: false}}

	referenced := business.DeclareSourceTrigger(source, live, nil)
	require.Equal(t, source.ID, referenced.Identity.SourceDelegation)
	require.True(t, referenced.Identity.Reference())

	// A revoked reference is not one: the declaration names no identity, which
	// an installing module refuses rather than running the work as itself.
	withoutIdentity := business.DeclareSourceTrigger(source, revoked, nil)
	require.False(t, withoutIdentity.Identity.Declared())

	source.ReconcileInterval = 15 * time.Minute
	edited := business.DeclareSourceTrigger(source, live, nil)
	require.NotEqual(t, referenced.Digest, edited.Digest, "an interval edit re-declares the trigger")

	// The next occurrence is computed from the declared cron, and nothing else:
	// the authoritative cursor belongs to the module that fires the trigger.
	require.NotNil(t, edited.NextOccurrenceAt)
	require.True(t, edited.NextOccurrenceAt.After(time.Now()))
	require.Zero(t, edited.NextOccurrenceAt.Minute()%15)
}

// THE HOST FIRES NOTHING. No code in this service may read a due-schedule set
// or enqueue reconcile work on a timer: the schedule is declared, and a host
// that also fired it would be two schedules for one source with the undeclared
// one winning.
//
// It is an AST walk and not a grep, for the reason a grep would miss: a
// commented-out sweep, a string in a doc comment and a test fixture all match
// the text and none of them fires anything, while a call behind a one-line
// wrapper matches no text at all. The parser is given no comments, so prose
// about the sweep — including this file's — cannot fail the gate, and cannot
// make it pass either.
func TestNoHostTimerFiresTheReconcileSchedule(t *testing.T) {
	// The producer that left, and the read it needed. Either name reappearing
	// in non-test source is the sweep coming back.
	forbidden := map[string]string{
		"RunDatasourceReconcile":               "the reconcile sweep: the schedule is declared, not fired here",
		"ListDatasourceSourcesDueForReconcile": "the due-schedule query the sweep read",
		"enqueueScheduledReconcile":            "the sweep's per-source dispatch",
		"enqueueScheduledPullSync":             "the sweep's pull-provider dispatch",
		"scheduledReconcileKey":                "the sweep's schedule-derived idempotency key",
	}
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	var found []string
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == "gen" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// No ParseComments: prose naming the sweep is not the sweep.
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		relative, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if reason, forbid := forbidden[identifier.Name]; forbid {
				found = append(found, relative+": "+identifier.Name+" — "+reason)
			}
			return true
		})
		return nil
	}))
	require.Empty(t, found, "this host must fire nothing on a source's reconcile schedule")
}
