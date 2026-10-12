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

// triggerContractFields are the field names and values an INSTALLATION
// DOCUMENT carries, as the module that fires triggers decodes one.
//
// They are PINNED here, by name and by value, because this host cannot verify
// them: the declaration leaves as JSON a composition folds into an
// installation document, and anything the decode does not accept is a release
// that will not install at all — not a schedule that visibly breaks.
//
// THE DISTINCTION THIS FILE EXISTS FOR. That module spells the same facts two
// different ways, and only one of them parses:
//
//	                 installation document     operator's read API
//	identity arms    source / binding          source_delegation / installation_binding
//	missed-run       "skip" / "catch_up"       MISSED_RUN_POLICY_SKIP / …_CATCH_UP
//	overlap          "skip" / "queue" / …      OVERLAP_POLICY_SKIP / …
//	deadline         deadline, nanoseconds     deadline_millis
//	arrival          event                     arrival
//
// A first version of this test pinned the right-hand column — the read API's
// names, which are the ones its proto publishes and therefore the ones a
// reader of that proto reaches for first. Every assertion passed, and the
// declaration it was guarding would have been refused at install on every
// field. So what is pinned here is the left-hand column, and the right-hand
// one is recorded above precisely so the next reader does not make the same
// substitution.
var triggerContractFields = struct {
	schedule   []string
	identity   []string
	missedRuns []string
	overlaps   []string
}{
	schedule:   []string{"cron", "timezone", "missed_run", "overlap", "deadline", "input"},
	identity:   []string{"source", "binding"},
	missedRuns: []string{"skip", "catch_up"},
	overlaps:   []string{"skip", "queue", "cancel_previous"},
}

// The declared schedule and identity serialize to exactly the document's field
// names — no more, no fewer, and none renamed.
func TestTriggerDeclarationMatchesTheContractFieldNames(t *testing.T) {
	schedule, err := json.Marshal(business.DeclaredTriggerSchedule{
		Cron: "*/30 * * * *", Timezone: "UTC",
		MissedRun: business.DatasourceTriggerMissedRun, Overlap: business.DatasourceTriggerOverlap,
		Deadline: time.Minute, Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	var scheduleFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schedule, &scheduleFields))
	require.ElementsMatch(t, triggerContractFields.schedule, keysOf(scheduleFields),
		"the schedule a release declares is decoded field by field by the module that fires it")

	// The deadline is an integer count of nanoseconds, not a duration string
	// and not milliseconds: the document is decoded by plain encoding/json
	// into a Go time.Duration, so "1m0s" fails the whole document and 60000
	// silently means sixty microseconds.
	require.JSONEq(t, "60000000000", string(scheduleFields["deadline"]))
	// And the input is the JSON value itself, not base64 of its bytes.
	require.JSONEq(t, "{}", string(scheduleFields["input"]))

	// Both arms are optional on the wire and exactly one is ever set, so each
	// is serialized on its own: a shape carrying both would be a declaration
	// with two answers, and one carrying neither is refused at install.
	for _, identity := range []business.DeclaredTriggerIdentity{
		{Source: "source"},
		{Binding: "binding"},
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
	require.False(t, business.DeclaredTriggerIdentity{Source: "s", Binding: "b"}.Declared(),
		"naming both arms is a declaration with two answers")
	require.True(t, business.DeclaredTriggerIdentity{Source: "s"}.Reference())
	require.False(t, business.DeclaredTriggerIdentity{Binding: "b"}.Reference())
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
	require.Contains(t, triggerContractFields.missedRuns, business.DatasourceTriggerMissedRun)
	require.Contains(t, triggerContractFields.overlaps, business.DatasourceTriggerOverlap)
	// Not the enum spelling an operator's read reports. A declaration carrying
	// it decodes to no policy at all.
	require.NotContains(t, business.DatasourceTriggerMissedRun, "MISSED_RUN_POLICY")
	require.NotContains(t, business.DatasourceTriggerOverlap, "OVERLAP_POLICY")
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
	require.Equal(t, source.ID, referenced.Identity.Source)
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
