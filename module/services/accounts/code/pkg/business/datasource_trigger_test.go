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
		declared := business.DeclareSourceTrigger(source(interval), nil, nil, true)
		require.True(t, declared.Declarable(), "%s: %s", interval, declared.UndeclarableReason)
		require.Equal(t, want, declared.Schedule.Cron, "interval %s", interval)
	}
	// A day, and a divisor of a day, are expressible; the minute (and hour) are
	// derived from the source so every source of one provider does not come due
	// in the same second.
	daily := business.DeclareSourceTrigger(source(24*time.Hour), nil, nil, true)
	require.True(t, daily.Declarable())
	require.Regexp(t, `^\d+ \d+ \* \* \*$`, daily.Schedule.Cron)
	twelveHourly := business.DeclareSourceTrigger(source(12*time.Hour), nil, nil, true)
	require.True(t, twelveHourly.Declarable())
	require.Regexp(t, `^\d+ \*/12 \* \* \*$`, twelveHourly.Schedule.Cron)

	// And the refusals, each by name rather than by approximation.
	for _, interval := range []time.Duration{
		25 * time.Minute, // restarts at the top of the hour: 25, 50, then 0
		90 * time.Second, // not a whole number of minutes
		5 * time.Hour,    // does not divide a day
		48 * time.Hour,   // beyond a day, which cron names by date, not by period
	} {
		declared := business.DeclareSourceTrigger(source(interval), nil, nil, true)
		require.False(t, declared.Declarable(), "interval %s was spelled %q", interval, declared.Schedule.Cron)
		require.Equal(t, business.TriggerUndeclarableInterval, declared.UndeclarableReason, "interval %s", interval)
	}
	disabled := business.DeclareSourceTrigger(&business.DatasourceSource{ID: "s", OrgID: "org", Status: "active"}, nil, nil, true)
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
	// The executing module's own delegation: its binding names itself as the
	// audience, which is the only shape a source-id reference can serve (see
	// executorReference below).
	live := []business.TriggerDelegationReference{executorReference(source.ID, true)}
	revoked := []business.TriggerDelegationReference{executorReference(source.ID, false)}

	referenced := business.DeclareSourceTrigger(source, live, nil, true)
	require.Equal(t, source.ID, referenced.Identity.Source)
	require.True(t, referenced.Identity.Reference())

	// A revoked reference is not one: the declaration names no identity, which
	// an installing module refuses rather than running the work as itself.
	withoutIdentity := business.DeclareSourceTrigger(source, revoked, nil, true)
	require.False(t, withoutIdentity.Identity.Declared())

	source.ReconcileInterval = 15 * time.Minute
	edited := business.DeclareSourceTrigger(source, live, nil, true)
	require.NotEqual(t, referenced.Digest, edited.Digest, "an interval edit re-declares the trigger")

	// The next occurrence is computed from the declared cron, and nothing else:
	// the authoritative cursor belongs to the module that fires the trigger.
	require.NotNil(t, edited.NextOccurrenceAt)
	require.True(t, edited.NextOccurrenceAt.After(time.Now()))
	require.Zero(t, edited.NextOccurrenceAt.Minute()%15)
}

// executorReference is a delegation a declared executor COULD use: the module
// holds it itself, its binding still confers something, and that binding names
// its own module as the audience.
func executorReference(sourceID string, active bool) business.TriggerDelegationReference {
	return selfAddressedReference(sourceID, "runtime", active)
}

// selfAddressedReference is the same shape under a named holder, for the cases
// that turn on WHICH module holds it.
func selfAddressedReference(sourceID, module string, active bool) business.TriggerDelegationReference {
	return business.TriggerDelegationReference{
		Reference: sourceID, DelegationID: "d-" + module, Module: module, Binding: "occurrences",
		Audience: module, Scopes: []string{"collections:write"}, Active: active,
	}
}

// ONLY THE EXECUTING MODULE'S DELEGATION IS NAMED (issue #1121, m5).
//
// The reference arm is a source id, and a mint by source id resolves the
// active delegation OF THE CALLING MODULE — so a delegation held by a module
// that does not fire this trigger can never admit one of its occurrences. It
// used to be taken anyway, as long as it was active and had a reference value,
// which is the worst shape a declaration can be in: the page reads "bound" and
// every occurrence is refused at the mint.
//
// Each case below is one of the three facts the resolution needs, removed on
// its own, so no single assertion carries the test.
func TestTriggerDeclarationNamesOnlyADelegationItsExecutorCanUse(t *testing.T) {
	next := time.Now().Add(time.Hour)
	source := &business.DatasourceSource{
		ID: "2a3f5c1e-0000-4000-8000-000000000003", OrgID: "org",
		Status: "active", ReconcileInterval: 30 * time.Minute, NextReconcileAt: &next,
	}

	usable := executorReference(source.ID, true)
	require.Equal(t, source.ID, business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{usable}, nil, true).Identity.Source)

	// A delegation held by one module and addressed to another. It is a real,
	// live delegation — the shape every consuming module's sync uses — and it
	// is reachable only through the delegation-id exchange, which no reference
	// this host declares can name.
	addressedElsewhere := usable
	addressedElsewhere.Audience = "somebody-else"

	// A delegation whose binding the module no longer declares: the read
	// resolves no scopes for it, and the next mint revokes it as
	// binding_changed. "Active" is not the same as "usable".
	bindingGone := usable
	bindingGone.Scopes = nil

	// And a delegation whose authority could not be resolved at all, which the
	// read reports as an empty audience rather than by omitting the row.
	unresolved := usable
	unresolved.Audience = ""

	for name, references := range map[string][]business.TriggerDelegationReference{
		"another module's delegation":       {addressedElsewhere},
		"a binding no longer declared":      {bindingGone},
		"an authority that did not resolve": {unresolved},
		// Several unusable ones do not add up to a usable one.
		"all of them together": {addressedElsewhere, bindingGone, unresolved},
	} {
		t.Run(name, func(t *testing.T) {
			declared := business.DeclareSourceTrigger(source, references, nil, true)
			require.True(t, declared.Declarable(), "the schedule is still declared; only the identity is missing")
			require.False(t, declared.Identity.Declared(),
				"a declaration naming an identity its executor cannot mint reads as bound and refuses every occurrence")
		})
	}

	// The usable one is found whatever order the read returns, and the
	// unusable ones never shadow it.
	mixed := business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{addressedElsewhere, usable}, nil, true)
	require.Equal(t, source.ID, mixed.Identity.Source)
}

// THE STOPGAP SWEEP IS CONFINED TO THE FILES THAT DELETE IT (issue #1112).
//
// The host fires this schedule again, as a labelled stopgap, because nothing
// consumes the declaration. That is only a stopgap if deleting it is one
// commit, and it is only one commit while every name belonging to it lives in
// the files RunDatasourceReconcile's deletion list names. A wrapper added
// somewhere else — a second caller, a second tick, a "just here too" helper —
// turns the deletion into an archaeology exercise and is exactly how a
// temporary schedule becomes a permanent second one.
//
// So this walk no longer forbids the names; it bounds WHERE they may appear.
// It is an AST walk and not a grep for the reason a grep would miss: prose
// about the sweep (including this file's, and the deletion list itself) matches
// the text and fires nothing, while a call behind a one-line wrapper matches no
// text at all. The parser is given no comments, so a doc comment naming the
// sweep can neither fail the gate nor satisfy it.
func TestTheStopgapSweepIsConfinedToTheFilesThatDeleteIt(t *testing.T) {
	// Each name, and the one non-test file it may be referenced from. These are
	// exactly the files RunDatasourceReconcile's deletion list names.
	confined := map[string]map[string]bool{
		"RunDatasourceReconcile":               {"business/datasource_deliveries.go": true, "work.go": true},
		"ListDatasourceSourcesDueForReconcile": {"business/datasource_deliveries.go": true, "business/store.go": true, "infra/postgres_datasource_sources.go": true},
		"enqueueScheduledReconcile":            {"business/datasource_deliveries.go": true},
		"enqueueScheduledPullSync":             {"business/datasource_deliveries.go": true},
		"scheduledReconcileKey":                {"business/datasource_deliveries.go": true},
		"datasourceReconcileBatchSize":         {"business/datasource_deliveries.go": true},
	}
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	// work.go sits beside pkg/ rather than inside it, so the walk starts one
	// level up from the service's packages.
	serviceRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	var escaped []string
	require.NoError(t, filepath.WalkDir(serviceRoot, func(path string, entry os.DirEntry, err error) error {
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
		relative, _ := filepath.Rel(serviceRoot, path)
		relative = strings.TrimPrefix(relative, "pkg/")
		ast.Inspect(file, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			allowed, tracked := confined[identifier.Name]
			if tracked && !allowed[relative] {
				escaped = append(escaped, relative+": "+identifier.Name)
			}
			return true
		})
		return nil
	}))
	require.Empty(t, escaped, "the stopgap sweep must stay deletable in one commit; see RunDatasourceReconcile's deletion list")
	// And the walk must actually be looking at the service: a root that
	// resolved to an empty tree would pass this test having checked nothing.
	require.DirExists(t, filepath.Join(root, "business"))
}

// The sweep enqueues for a source that is due and active, and for no other —
// asserted on the jobs it produced, not on the count it returned, because a
// count is the sweep agreeing with itself.
func TestStopgapSweepEnqueuesOnlyDueActiveSources(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	due := githubSource(t, svc, "main", nil, "")
	later := githubSource(t, svc, "main", nil, "")
	parked := githubSource(t, svc, "main", nil, "")
	setNextReconcile(t, store, due.ID, time.Now().Add(-time.Minute))
	setNextReconcile(t, store, later.ID, time.Now().Add(time.Hour))
	setNextReconcile(t, store, parked.ID, time.Time{}) // nil = off its schedule

	_, err := svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)

	require.Len(t, jobsFor(producer, due.ID), 1, "the due source was not reconciled")
	require.Empty(t, jobsFor(producer, later.ID), "a source not yet due was reconciled")
	require.Empty(t, jobsFor(producer, parked.ID), "a source off its schedule was reconciled")

	job := jobsFor(producer, due.ID)[0]
	require.Equal(t, business.DatasourceDeliveryQueue, job.GetQueue())
	require.Equal(t, business.DatasourceReconcileTopic, job.GetTopic())
	require.Equal(t, "conditional", job.GetAttributes()["datasource.reconcile_mode"])
	// A message on exactly the same terms as every other enqueue: the platform
	// validates content_type (min_len 1), and a sweep job it refuses would fail
	// without rescheduling, so the same due batch would stall forever at WARN.
	require.Equal(t, "application/json", job.GetContentType())
	require.Equal(t, "{}", string(job.GetPayload()))
	require.EqualValues(t, 1, job.GetSchemaVersion())

	// And the pass moved the schedule, so the next tick does not pick the same
	// row again.
	require.True(t, storedSource(t, store, due.ID).NextReconcileAt.After(time.Now()),
		"the sweep did not reschedule the source it enqueued")
}

// A pull provider goes to the pull engine the connector has, with the
// provenance that tells an operator the schedule took the pass rather than a
// person — the same two engines one declared occurrence reaches.
func TestStopgapSweepRoutesAPullProviderToItsOwnEngine(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{})

	source := newCrawlerSource(t, svc)
	takeConnectSync(t, producer, source)
	setNextReconcile(t, store, source.ID, time.Now().Add(-time.Minute))

	_, err := svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)

	jobs := jobsFor(producer, source.ID)
	require.Len(t, jobs, 1)
	require.Equal(t, business.DatasourceSyncRequestQueue, jobs[0].GetQueue())
	require.Equal(t, business.DatasourceScheduledSyncSource, jobs[0].GetSource())
}

// Two replicas sweeping one due row before either bumps it enqueue ONE job
// between them, because the key is the row's due instant rather than a fresh
// identity. For a pull provider a duplicate is a second full re-send of the
// whole source, with no cursor to make the second one cheap.
func TestStopgapSweepKeyIsTheDueInstantSoTwoReplicasAgree(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{})

	source := newUploadSource(t, svc)
	takeConnectSync(t, producer, source)
	due := time.Now().Add(-time.Minute).UTC()

	// Two passes over the same due instant: the bump is undone between them, so
	// both see the row exactly as a second replica would.
	setNextReconcile(t, store, source.ID, due)
	_, err := svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)
	setNextReconcile(t, store, source.ID, due)
	_, err = svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)

	keys := jobKeys(producer, source.ID)
	require.Len(t, keys, 2, "expected one job per pass in the recording")
	require.Equal(t, keys[0], keys[1],
		"two replicas on one due row produced different keys; the platform would run the pull twice")

	// A different due instant is different work and must not be swallowed as a
	// replay of the one before it. (Earlier rather than later only so the row
	// is still due when the sweep reads it; the key cares which instant, not
	// which direction.)
	setNextReconcile(t, store, source.ID, due.Add(-time.Hour))
	_, err = svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)
	later := jobKeys(producer, source.ID)
	require.Len(t, later, 3)
	require.NotEqual(t, keys[0], later[2], "the next scheduled pass reused the previous instant's key")
}

// The stopgap does not change what the source DECLARES. That is what lets the
// two coexist until the declaration is consumed: the declaration reads the
// stored instant's presence as "on its schedule" and never its value, and its
// own next occurrence comes from the cron. A sweep that moved the declaration
// would be two schedules disagreeing rather than one firing twice.
func TestStopgapSweepLeavesTheDeclarationUnchanged(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	source := githubSource(t, svc, "main", nil, "")
	setNextReconcile(t, store, source.ID, time.Now().Add(-time.Minute))
	before := business.DeclareSourceTrigger(storedSource(t, store, source.ID), nil, nil, true)
	require.True(t, before.Declarable())

	_, err := svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)

	after := business.DeclareSourceTrigger(storedSource(t, store, source.ID), nil, nil, true)
	require.True(t, after.Declarable(), "the sweep took the source off its declared schedule")
	require.Equal(t, before.Schedule.Cron, after.Schedule.Cron)
	require.Equal(t, before.Digest, after.Digest, "the sweep moved the declaration an installation pinned")
}

// A SELF-ADDRESSED DELEGATION DOES NOT IDENTIFY THE EXECUTOR (issue #1121, F1).
//
// The predicate above establishes the SHAPE a source-id reference can serve. It
// cannot establish who will execute the release, because nothing this host
// holds declares that: a self-addressed binding is generic and a deployment may
// have several. So the counterexample the review built is real — module A holds
// a self-addressed delegation on source S, the composed executor is B, which
// holds none, and B's mint resolves ActiveSourceDelegation(S, B) and is refused.
//
// What this host must therefore NOT do is tell an operator that S is ready. It
// names the candidate, reports WHOSE delegation it is, and claims nothing about
// the executor. These cases vary the holder independently of any executor, which
// is the axis the first version of this test did not vary at all.
func TestTriggerDeclarationNamesTheHolderAndClaimsNoExecutor(t *testing.T) {
	next := time.Now().Add(time.Hour)
	source := &business.DatasourceSource{
		ID: "2a3f5c1e-0000-4000-8000-000000000005", OrgID: "org",
		Status: "active", ReconcileInterval: 30 * time.Minute, NextReconcileAt: &next,
	}
	// Two modules, each self-addressed, which the registry accepts generically:
	// A holds a delegation on this source, B does not.
	holderA := selfAddressedReference(source.ID, "module-a", true)

	// A ONLY: the declaration names the candidate and says it is A's. It must
	// not be read as "the executor holds one" — if the composed executor is B,
	// every occurrence is refused at the mint, and this host cannot tell.
	onlyA := business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{holderA}, nil, true)
	require.Equal(t, source.ID, onlyA.Identity.Source, "the candidate is still declared; it is the only shape a reference can take")
	require.Equal(t, "module-a", onlyA.IdentityHolder, "a reader must be told whose delegation the reference resolves to")

	// B's own current delegation yields the positive claim under B's name —
	// the same declared reference value, a different holder, which is exactly
	// the fact the arm cannot carry and the read must.
	holderB := selfAddressedReference(source.ID, "module-b", true)
	onlyB := business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{holderB}, nil, true)
	require.Equal(t, source.ID, onlyB.Identity.Source)
	require.Equal(t, "module-b", onlyB.IdentityHolder)
	require.Equal(t, onlyA.Identity.Source, onlyB.Identity.Source,
		"the reference value is the source id, so A's and B's are indistinguishable in the arm — which is why the holder is reported beside it")

	// Revoking B's delegation removes the claim entirely rather than falling
	// back to A: a declaration is built from the references as they are.
	revokedB := selfAddressedReference(source.ID, "module-b", false)
	goneB := business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{revokedB}, nil, true)
	require.False(t, goneB.Identity.Declared(), "a revoked delegation is not a candidate")
	require.Empty(t, goneB.IdentityHolder, "no identity means no holder to name")

	// And a binding B's module no longer declares: the read resolves no scopes,
	// so the next mint would revoke it as binding_changed.
	staleB := selfAddressedReference(source.ID, "module-b", true)
	staleB.Scopes = nil
	changedB := business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{staleB}, nil, true)
	require.False(t, changedB.Identity.Declared())
	require.Empty(t, changedB.IdentityHolder)

	// With both present the holder named is one of the two that actually hold
	// one — never a module that holds none.
	both := business.DeclareSourceTrigger(source, []business.TriggerDelegationReference{holderA, holderB}, nil, true)
	require.Contains(t, []string{"module-a", "module-b"}, both.IdentityHolder)
}

// THE STOPGAP SWEEP HAS A SWITCH, AND IT IS ONE FACT (issue #1121, N7).
//
// The declaration's consumer and the sweep must never both run: they enqueue
// under different idempotency keys, so nothing collapses them, and the
// per-source FIFO key only makes the second serialise behind the first — a
// second full re-send for a pull provider. Before this there was nothing to
// turn off but a redeployed, patched host.
//
// The switch is read in two places and they must agree: the tick is not
// installed AND the sweep itself refuses. A host that only dropped the tick
// would still sweep from any other caller, and would still describe a sweep
// that is not running.
func TestTheStopgapSweepCanBeSwitchedOff(t *testing.T) {
	producer := &recordingProducer{}
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	source := githubSource(t, svc, "main", nil, "")
	due := time.Now().Add(-time.Minute)
	setNextReconcile(t, store, source.ID, due)

	// Default ON: that is what every deployment does today, and a default that
	// silently stopped every scheduled reconcile is the outage #1090 caused.
	require.True(t, svc.DatasourceReconcileSweepEnabled())

	svc.SetDatasourceReconcileSweep(false)
	require.False(t, svc.DatasourceReconcileSweepEnabled())
	enqueued, err := svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err, "a disabled sweep is not an error; it is a sweep that does not happen")
	require.Zero(t, enqueued)
	require.Empty(t, jobKeys(producer, source.ID), "a disabled sweep enqueued work")

	// And it did not move the source's schedule either: a host that is not
	// firing must not consume the instant something else fires on.
	require.Equal(t, due.UTC().Truncate(time.Second),
		storedSource(t, store, source.ID).NextReconcileAt.UTC().Truncate(time.Second),
		"a disabled sweep bumped the schedule")

	// The declaration says which of the two is acting, and the instant each
	// acts on. They share a period and NOT a phase: the cron comes due on the
	// half hour while the sweep fires on the stored instant.
	off := business.DeclareSourceTrigger(storedSource(t, store, source.ID), nil, nil, false)
	require.False(t, off.HostSweepEnabled)
	require.Nil(t, off.HostSweepDueAt, "a host that takes no pass must not name an instant for one")
	require.NotNil(t, off.NextOccurrenceAt, "the declaration is unchanged by the switch")

	on := business.DeclareSourceTrigger(storedSource(t, store, source.ID), nil, nil, true)
	require.True(t, on.HostSweepEnabled)
	require.NotNil(t, on.HostSweepDueAt)
	require.Equal(t, due.UTC().Truncate(time.Second), on.HostSweepDueAt.UTC().Truncate(time.Second),
		"the sweep's instant is the stored one, not the cron's")
	require.NotEqual(t, on.HostSweepDueAt.Truncate(time.Minute), on.NextOccurrenceAt.Truncate(time.Minute),
		"fixture: the two instants must differ, which is the whole point of reporting both")

	// Switched back on, the same due row is swept.
	svc.SetDatasourceReconcileSweep(true)
	enqueued, err = svc.RunDatasourceReconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, enqueued)
}

// A parked source names no sweep instant either: the stored instant is cleared,
// which is what the park and restore paths use as the switch, and there is then
// no pass for this host to take.
func TestAParkedSourceNamesNoSweepInstant(t *testing.T) {
	parked := &business.DatasourceSource{
		ID: "2a3f5c1e-0000-4000-8000-000000000004", OrgID: "org",
		Status: "active", ReconcileInterval: 30 * time.Minute,
	}
	declared := business.DeclareSourceTrigger(parked, nil, nil, true)
	require.False(t, declared.Declarable())
	require.True(t, declared.HostSweepEnabled, "the switch is a deployment fact, not a per-source one")
	require.Nil(t, declared.HostSweepDueAt)
}

// One occurrence's enqueue key names the ORGANIZATION and the SOURCE as well as
// the effect (issue #1112, m2 — and N4 on PR #1115, which found the fix
// untested).
//
// An effect identity is the CALLER's to choose and the GitHub reconcile job's
// scope is Global, so a key of the effect alone put every tenant's reconciles
// in one namespace: a caller in one organization could pick an identity already
// pending for a source in another and have the platform collapse the two,
// suppressing that organization's reconcile with no refusal anywhere. The test
// asserts the three parts are all present AND that two organizations choosing
// the same identity do not collide — the second half being the defect, which an
// assertion on the key's shape alone would not catch.
func TestReconcileOccurrenceKeyIsScopedByOrgAndSource(t *testing.T) {
	producer := &recordingProducer{}
	store := newReconcileOperationStore()
	svc, _ := newDatasourceService(store, producer, &fakeGitHub{defaultBranch: "main", commit: "abc"})

	// Two sources in two DIFFERENT organizations, which is the scenario: the
	// GitHub reconcile job's scope is Global, so one key namespace spans every
	// tenant. Within one organization the attempt row already binds an effect
	// identity to its source and refuses a second use, so a same-org test
	// would pass for the wrong reason.
	const otherOrg = "22222222-2222-4222-8222-222222222222"
	first := newUploadSource(t, svc)
	takeConnectSync(t, producer, first)
	second := newUploadSourceIn(t, svc, otherOrg)
	takeConnectSync(t, producer, second)
	require.NotEqual(t, first.OrgID, second.OrgID, "fixture: the two sources must be in different organizations")

	// The SAME caller-chosen identity, in each organization.
	const chosen = "an identity the caller chose"
	reconcileOnce(t, svc, first.OrgID, first.ID, chosen)
	reconcileOnce(t, svc, second.OrgID, second.ID, chosen)

	firstKey := jobKeys(producer, first.ID)
	secondKey := jobKeys(producer, second.ID)
	require.Len(t, firstKey, 1)
	require.Len(t, secondKey, 1)
	require.NotEqual(t, firstKey[0], secondKey[0],
		"two organizations' sources under one caller-chosen identity produced the same key; the platform would collapse them and one tenant's reconcile would be suppressed with no refusal anywhere")

	// Each part is named, so a future key change cannot drop one silently.
	require.Contains(t, firstKey[0], first.OrgID, "the key does not name the organization")
	require.Contains(t, firstKey[0], first.ID, "the key does not name the source")
	require.Contains(t, firstKey[0], chosen, "the key does not name the effect")

	// And the idempotency the operation DOES need still holds: two attempts of
	// one occurrence on one source agree.
	reconcileOnce(t, svc, first.OrgID, first.ID, chosen)
	again := jobKeys(producer, first.ID)
	require.Len(t, again, 2)
	require.Equal(t, again[0], again[1],
		"two attempts of one occurrence produced different keys; the pull would run twice")
}
