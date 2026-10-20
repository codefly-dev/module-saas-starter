package business_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// editableSource is a connected GitHub source with a scope worth narrowing, and
// the first sync the connect enqueued forgotten so each test counts only the
// jobs its own edit produced.
func editableSource(t *testing.T, svc *business.Service) *business.DatasourceSource {
	t.Helper()
	return addSource(t, svc, business.AddGitHubSourceInput{
		OrgID:           testOrg,
		Repo:            "acme/handbook",
		Paths:           []string{"docs"},
		FileExtensions:  []string{".md"},
		Branch:          "main",
		CollectionLabel: "handbook",
		AccessToken:     "ghp_secret",
	})
}

// scopeOf is the expectation an edit carries: the scope its author was shown.
// Every UpdateSource below states one, because the host refuses an edit that
// does not — a whole-field replacement applied unconditionally is how one
// editor's save erases another's.
func scopeOf(source *business.DatasourceSource) *business.DatasourceFetchScope {
	return &business.DatasourceFetchScope{
		Branch:         source.Branch,
		Paths:          source.Paths,
		FileExtensions: source.FileExtensions,
	}
}

// The whole point of UpdateSource: the branch and filters move on the SAME row,
// so the credential envelope, the collection and the repository are exactly
// where they were. Editing used to mean delete plus re-add, which drops all
// three along with the sync cursor.
func TestUpdateSource_MovesTheScopeAndKeepsTheSourcesIdentity(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(store, producer, nil)
	source := editableSource(t, svc)

	updated, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID:  testOrg,
		ID:     source.ID,
		Branch: "release",
		// Duplicates and slashes are normalized by the connect path's own
		// normalizePaths, so an edit cannot store a scope a connect would refuse.
		Paths:          []string{"/docs/", "docs", "guides"},
		FileExtensions: []string{".MD", ".mdx"},
		Expected:       scopeOf(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != source.ID || updated.Repo != source.Repo ||
		updated.BoundaryNodeID != source.BoundaryNodeID ||
		updated.CredentialSecretRef != source.CredentialSecretRef {
		t.Fatalf("identity moved: %+v", updated)
	}
	if updated.Branch != "release" {
		t.Fatalf("branch = %q, want release", updated.Branch)
	}
	if want := []string{"docs", "guides"}; !reflect.DeepEqual(updated.Paths, want) {
		t.Fatalf("paths = %v, want %v", updated.Paths, want)
	}
	if want := []string{".md", ".mdx"}; !reflect.DeepEqual(updated.FileExtensions, want) {
		t.Fatalf("file extensions = %v, want %v", updated.FileExtensions, want)
	}

	// The read-back proves the row was written, not just the struct returned.
	stored, err := svc.GetDatasourceSource(context.Background(), testOrg, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Branch != "release" || !reflect.DeepEqual(stored.FileExtensions, []string{".md", ".mdx"}) {
		t.Fatalf("stored source = %+v", stored)
	}

	// The move owes the consuming module a re-snapshot, named as one.
	if jobID == "" {
		t.Fatal("no sync was enqueued for a change that moved what is fetched")
	}
	jobs := producer.reconcileJobsFor(source.ID)
	if len(jobs) != 1 {
		t.Fatalf("reconcile jobs = %d, want 1", len(jobs))
	}
	if mode := jobs[0].GetAttributes()["datasource.reconcile_mode"]; mode != "reconfigured" {
		t.Fatalf("reconcile mode = %q, want reconfigured", mode)
	}

	entries := audit.entriesOf(business.EventDatasourceSourceUpdated)
	if len(entries) != 1 {
		t.Fatalf("updated events = %d, want 1", len(entries))
	}
	want := map[string]any{
		"repo":            "acme/handbook",
		"branch":          "release",
		"previous_branch": "main",
		"paths":           []string{"docs", "guides"},
		"file_extensions": []string{".md", ".mdx"},
		// The collection the re-snapshot replaces, so a collection grant reaches
		// this record (isCollectionBoundaryEvent reads the declaration).
		"boundary": source.BoundaryNodeID,
	}
	if !reflect.DeepEqual(entries[0].Payload, want) {
		t.Fatalf("payload = %v, want %v", entries[0].Payload, want)
	}
	requireDeclaredPayloads(t, audit)
}

// A sync re-sends the whole source to the consuming module, so submitting the
// values the source already holds must not start one. Normalization happens
// first: the form's "/docs/" is the stored "docs", and comparing before
// normalizing would call that a change.
func TestUpdateSource_UnchangedScopeWritesNothing(t *testing.T) {
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	source := editableSource(t, svc)

	_, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID:          testOrg,
		ID:             source.ID,
		Branch:         " main ",
		Paths:          []string{"/docs/"},
		FileExtensions: []string{".md"},
		Expected:       scopeOf(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "" {
		t.Fatalf("job id = %q, want empty: nothing changed", jobID)
	}
	if jobs := producer.reconcileJobsFor(source.ID); len(jobs) != 0 {
		t.Fatalf("reconcile jobs = %d, want 0", len(jobs))
	}
	if entries := audit.entriesOf(business.EventDatasourceSourceUpdated); len(entries) != 0 {
		t.Fatalf("updated events = %d, want 0: nothing changed", len(entries))
	}
}

// A branch the source's credential cannot read must be refused BEFORE the row
// is written. Persisted, it would leave the source reading active while every
// sync from then on failed.
func TestUpdateSource_RefusesAnUnreadableBranchWithoutWriting(t *testing.T) {
	gh := &fakeGitHub{defaultBranch: "main", commit: "abc"}
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := editableSource(t, svc)

	gh.resolveErr = github.ErrNotFound
	_, _, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Branch: "gone", Paths: source.Paths, FileExtensions: source.FileExtensions,
		Expected: scopeOf(source),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("error = %v, want FailedPrecondition", err)
	}
	gh.resolveErr = nil

	stored, err := svc.GetDatasourceSource(context.Background(), testOrg, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Branch != "main" {
		t.Fatalf("stored branch = %q, want main: the refusal must not have written", stored.Branch)
	}
	if entries := audit.entriesOf(business.EventDatasourceSourceUpdated); len(entries) != 0 {
		t.Fatalf("updated events = %d, want 0", len(entries))
	}
	if jobs := producer.reconcileJobsFor(source.ID); len(jobs) != 0 {
		t.Fatalf("reconcile jobs = %d, want 0", len(jobs))
	}
}

// Narrowing the filters alone never asks GitHub anything: the branch did not
// move, so there is nothing new to prove readable, and a GitHub outage must not
// block an edit that cannot depend on it.
func TestUpdateSource_FilterOnlyEditNeedsNoBranchValidation(t *testing.T) {
	gh := &fakeGitHub{defaultBranch: "main", commit: "abc"}
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(newDatasourceFakeStore(), producer, gh)
	source := addSource(t, svc, business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/handbook", Paths: []string{"docs"},
		Branch: "main", CollectionLabel: "handbook", AccessToken: "ghp_secret",
	})
	// Only after the connect: GitHub answers nothing from here on, and the edit
	// below must still succeed because it asks GitHub nothing.
	gh.resolveErr = github.ErrNotFound

	updated, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Branch: "main", Paths: []string{"docs", "guides"},
		Expected: scopeOf(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	if jobID == "" || len(updated.Paths) != 2 {
		t.Fatalf("job id = %q, paths = %v", jobID, updated.Paths)
	}
	// The branch stayed, so the trail records no previous one to compare against.
	entries := audit.entriesOf(business.EventDatasourceSourceUpdated)
	if len(entries) != 1 {
		t.Fatalf("updated events = %d, want 1", len(entries))
	}
	if _, ok := entries[0].Payload["previous_branch"]; ok {
		t.Fatalf("payload names a previous branch though the branch did not move: %v", entries[0].Payload)
	}
	requireDeclaredPayloads(t, audit)
}

func TestUpdateSource_Validation(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	source := editableSource(t, svc)

	for name, tc := range map[string]struct {
		input business.UpdateSourceInput
		code  codes.Code
	}{
		"no source id": {
			input: business.UpdateSourceInput{OrgID: testOrg, Expected: scopeOf(source)},
		},
		"unknown source": {
			input: business.UpdateSourceInput{OrgID: testOrg, ID: business.NewIDString(), Expected: scopeOf(source)},
		},
		// Nothing to compare the replacement against. Refused rather than
		// applied: an unconditional whole-field write is the defect, not a
		// convenience.
		"no expected scope": {
			input: business.UpdateSourceInput{OrgID: testOrg, ID: source.ID, Branch: "release"},
			code:  codes.InvalidArgument,
		},
		// The connect path's own extension rule, not a second one.
		"a path is not a file suffix": {
			input: business.UpdateSourceInput{OrgID: testOrg, ID: source.ID, FileExtensions: []string{"docs/*.md"}, Expected: scopeOf(source)},
			code:  codes.InvalidArgument,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := svc.UpdateSource(context.Background(), "actor-1", tc.input)
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.code != codes.OK && status.Code(err) != tc.code {
				t.Fatalf("code = %s, want %s", status.Code(err), tc.code)
			}
		})
	}

	// A source that is not there is reported as such rather than as a refusal to
	// edit something: the handler maps it to NotFound.
	_, _, err := svc.UpdateSource(context.Background(), "actor-1",
		business.UpdateSourceInput{OrgID: testOrg, ID: business.NewIDString(), Expected: scopeOf(source)})
	if !errors.Is(err, business.ErrDatasourceSourceNotFound) {
		t.Fatalf("error = %v, want ErrDatasourceSourceNotFound", err)
	}
}

// Branch and path filters are the GitHub connector's config; the other
// providers are configured by their own fields. Accepting a branch for one of
// them would report a change that changed nothing.
func TestUpdateSource_RefusesANonGitHubSource(t *testing.T) {
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, &recordingProducer{}, nil)
	// Inserted rather than connected: whether a provider is admitted for a NEW
	// source is a separate decision, and this asserts the refusal for one that
	// is already stored.
	crawler := &business.DatasourceSource{
		ID:             business.NewIDString(),
		OrgID:          testOrg,
		Provider:       business.DatasourceProviderCrawler,
		Status:         business.DatasourceStatusActive,
		BoundaryNodeID: business.NewIDString(),
		Crawler:        &business.CrawlerDatasourceConfig{SitemapURL: "https://docs.example.com/sitemap.xml"},
	}
	if err := store.InsertDatasourceSource(context.Background(), crawler); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: crawler.ID, Branch: "main", Expected: scopeOf(crawler),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("error = %v, want FailedPrecondition", err)
	}
}

// The sync a reconfigure enqueues is a FORCED reconcile: it takes a full
// snapshot regardless of the cursor, which is what makes the new scope reach
// the consuming module instead of the compiler finding the head unmoved and
// handing nothing off.
func TestUpdateSource_ReconfiguredModeForcesTheSnapshot(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	source := editableSource(t, svc)
	if _, _, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Branch: "release", Expected: scopeOf(source),
	}); err != nil {
		t.Fatal(err)
	}
	jobs := producer.reconcileJobsFor(source.ID)
	if len(jobs) != 1 {
		t.Fatalf("reconcile jobs = %d, want 1", len(jobs))
	}
	progress := business.ProjectDatasourceSyncProgress(business.DatasourceSyncRecord{
		JobID:         "job-1",
		Topic:         business.DatasourceReconcileTopic,
		ReconcileMode: jobs[0].GetAttributes()["datasource.reconcile_mode"],
		State:         jobsv1.JobState_JOB_STATE_PENDING,
	}, business.DatasourceSyncHandoff{})
	if progress.Trigger != business.DatasourceSyncTriggerReconfigured {
		t.Fatalf("trigger = %v, want reconfigured — a reader cannot otherwise tell this change set covers a new scope", progress.Trigger)
	}
}

// The row can be deleted between the read UpdateSource validates against and
// the statement that writes it — they are separate transactions, and under READ
// COMMITTED the second simply matches nothing. Both halves of the write path
// refuse it, and each is asserted on its own, because a caller that commits an
// audit record and queues a reconcile after an empty result does both for a
// source that is gone and answers as though the edit had landed.
//
// Deleting the row BEFORE the call would prove none of this: UpdateSource's own
// first read would answer not-found and neither statement would run. The
// deletion is interleaved into the window a caller actually has.
func TestUpdateSource_RefusesWhenTheSourceWasDeletedBeforeTheWrite(t *testing.T) {
	for name, statement := range map[string]string{
		"gone by the locked read": "LockDatasourceSourceFetchScope",
		"gone by the update":      "UpdateDatasourceSourceFetchScope",
	} {
		t.Run(name, func(t *testing.T) {
			store := newDatasourceFakeStore()
			producer := &recordingProducer{}
			svc, audit := newDatasourceService(store, producer, nil)
			source := editableSource(t, svc)
			store.interleave(statement, func() { store.deleteSourceRow(source.ID) })

			_, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
				OrgID: testOrg, ID: source.ID, Branch: "release", Expected: scopeOf(source),
			})
			if !errors.Is(err, business.ErrDatasourceSourceNotFound) {
				t.Fatalf("error = %v, want ErrDatasourceSourceNotFound", err)
			}
			if jobID != "" {
				t.Fatalf("job id = %q, want empty", jobID)
			}
			if jobs := producer.reconcileJobsFor(source.ID); len(jobs) != 0 {
				t.Fatalf("reconcile jobs = %d, want 0: a deleted source has nothing to re-snapshot", len(jobs))
			}
			if entries := audit.entriesOf(business.EventDatasourceSourceUpdated); len(entries) != 0 {
				t.Fatalf("updated events = %d, want 0: the trail must not record an edit to a source that is gone", len(entries))
			}
		})
	}
}

// Every field is a whole-field replacement, so a save states the source's
// complete scope. Applied late, that silently reverts whatever replaced what
// the editor was shown — here, someone else's narrowing — and re-ingests
// everything they had just excluded. The expectation is what refuses it.
func TestUpdateSource_RefusesAnEditWhoseExpectedScopeMoved(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &recordingProducer{}
	svc, audit := newDatasourceService(store, producer, nil)
	source := editableSource(t, svc)
	opened := scopeOf(source)

	// Someone else narrows the source while the first editor's form is open.
	if _, _, err := svc.UpdateSource(context.Background(), "actor-2", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Branch: "main", Paths: []string{"docs/internal"},
		FileExtensions: source.FileExtensions, Expected: opened,
	}); err != nil {
		t.Fatal(err)
	}

	// The first editor saves, carrying the scope they were shown. They only
	// meant to move the branch, but their request also restates the wider paths.
	_, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Branch: "release", Paths: opened.Paths,
		FileExtensions: opened.FileExtensions, Expected: opened,
	})
	if !errors.Is(err, business.ErrDatasourceSourceScopeChanged) {
		t.Fatalf("error = %v, want ErrDatasourceSourceScopeChanged", err)
	}
	if jobID != "" {
		t.Fatalf("job id = %q, want empty", jobID)
	}

	// The narrowing stands, and only the one edit that was accepted is recorded.
	stored, err := svc.GetDatasourceSource(context.Background(), testOrg, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/internal"}; !reflect.DeepEqual(stored.Paths, want) {
		t.Fatalf("paths = %v, want %v: the refused save overwrote the narrowing", stored.Paths, want)
	}
	if stored.Branch != "main" {
		t.Fatalf("branch = %q, want main: the refused save was applied", stored.Branch)
	}
	if entries := audit.entriesOf(business.EventDatasourceSourceUpdated); len(entries) != 1 {
		t.Fatalf("updated events = %d, want 1 (the accepted edit only)", len(entries))
	}
	if jobs := producer.reconcileJobsFor(source.ID); len(jobs) != 1 {
		t.Fatalf("reconcile jobs = %d, want 1 (the accepted edit only)", len(jobs))
	}
}

// Re-submitting the values a form was filled with is a no-op even when the row
// holds a spelling today's normalizers would not produce. An expectation is
// only ever compared, never stored, so holding it to the strict rule would make
// such a source impossible to edit at all.
func TestUpdateSource_ComparesAnExpectationLenientlyEnoughToEditALegacyRow(t *testing.T) {
	store := newDatasourceFakeStore()
	svc, _ := newDatasourceService(store, &recordingProducer{}, nil)
	source := editableSource(t, svc)
	// A row as an older normalizer left it: an untrimmed prefix and an uppercase
	// suffix, neither of which normalizeFileExtensions would accept today.
	store.setSourceScope(source.ID, "main", []string{"docs/"}, []string{".MD"})

	updated, jobID, err := svc.UpdateSource(context.Background(), "actor-1", business.UpdateSourceInput{
		OrgID: testOrg, ID: source.ID, Branch: "main", Paths: []string{"docs", "guides"},
		FileExtensions: []string{".md"},
		Expected: &business.DatasourceFetchScope{
			Branch: "main", Paths: []string{"docs/"}, FileExtensions: []string{".MD"},
		},
	})
	if err != nil {
		t.Fatalf("a legacy spelling made the source un-editable: %v", err)
	}
	if jobID == "" {
		t.Fatal("no sync was enqueued for a change that moved what is fetched")
	}
	if want := []string{"docs", "guides"}; !reflect.DeepEqual(updated.Paths, want) {
		t.Fatalf("paths = %v, want %v", updated.Paths, want)
	}
}
