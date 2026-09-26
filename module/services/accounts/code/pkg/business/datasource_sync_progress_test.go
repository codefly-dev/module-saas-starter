package business_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// connectProducers lets addSource drop the first sync AddGitHubSource enqueues
// at connect, so a test that measures what follows the connect counts only
// that. TestAddGitHubSourceStartsItsFirstSyncAtConnect measures the connect.
var connectProducers sync.Map // *business.Service → *recordingProducer

func forgetConnectSync(svc *business.Service, sourceID string) {
	value, ok := connectProducers.Load(svc)
	if !ok {
		return
	}
	p := value.(*recordingProducer)
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.jobs); n > 0 && isConnectSync(p.jobs[n-1], sourceID) {
		p.jobs = p.jobs[:n-1]
	}
}

func isConnectSync(job *jobsv1.NewJob, sourceID string) bool {
	return job.GetQueue() == business.DatasourceDeliveryQueue &&
		job.GetTopic() == business.DatasourceReconcileTopic &&
		job.GetAttributes()["datasource.source_id"] == sourceID &&
		job.GetAttributes()["datasource.reconcile_mode"] == "force"
}

func TestAddGitHubSourceStartsItsFirstSyncAtConnect(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	source, err := svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "guides", AccessToken: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(producer.jobs) != 1 || !isConnectSync(producer.jobs[0], source.ID) {
		t.Fatalf("connect must enqueue exactly the source's first sync, a forced reconcile; got %d jobs", len(producer.jobs))
	}
	job := producer.jobs[0]
	if job.GetOrdering().GetComponents()[0] != source.ID || job.GetAttributes()["datasource.org_id"] != testOrg {
		t.Fatalf("first sync not bound to the source and org: %v", job)
	}
}

// A source is committed before its first sync is enqueued, so a queue that
// refuses the job must not undo the connect: the periodic reconcile syncs it.
func TestAddGitHubSourceSurvivesAFirstSyncThatCannotBeQueued(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	svc.SetDatasourceConnector(purposeCipher{}, failedSyncProducer{}, "")
	if _, err := svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "guides", AccessToken: "t",
	}); err != nil {
		t.Fatalf("connect failed because its first sync could not be queued: %v", err)
	}
}

func snapshotAttrs(t *testing.T, producer *recordingProducer) map[string]string {
	t.Helper()
	for i := len(producer.jobs) - 1; i >= 0; i-- {
		if producer.jobs[i].GetTopic() == business.DatasourceSnapshotTopic {
			return producer.jobs[i].GetAttributes()
		}
	}
	t.Fatal("no snapshot job enqueued")
	return nil
}

func wantCounts(t *testing.T, attrs map[string]string, files, added, modified, deleted string) {
	t.Helper()
	got := [5]string{attrs["datasource.changes.files"], attrs["datasource.changes.added"], attrs["datasource.changes.modified"], attrs["datasource.changes.deleted"], attrs["datasource.changes.split_known"]}
	want := [5]string{files, added, modified, deleted, "true"}
	if got != want {
		t.Fatalf("change counts = %v, want %v", got, want)
	}
}

// The snapshot job carries what the compiler found, relative to the commit the
// host last handed off: a first sync adds every file, a forced re-sync of an
// unchanged head changes none, and a moved head is split by a local diff.
func TestSnapshotCarriesItsChangeCounts(t *testing.T) {
	files := []github.File{{Path: "docs/a.md", SHA: "sa"}, {Path: "docs/b.md", SHA: "sb"}, {Path: "docs/c.md", SHA: "sc"}}
	newService := func(compare func(base, head string) (*github.Comparison, error)) (*business.Service, *recordingProducer) {
		producer := &recordingProducer{}
		gh := &fakeGitHub{commit: "HEAD", files: files, compareFn: compare}
		svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, gh)
		return svc, producer
	}

	t.Run("first sync", func(t *testing.T) {
		svc, producer := newService(nil)
		source := githubSource(t, svc, "main", []string{"docs"}, "")
		if _, err := svc.ReconcileGitHubSource(context.Background(), source, true, "11111111-1111-1111-1111-111111111111"); err != nil {
			t.Fatal(err)
		}
		wantCounts(t, snapshotAttrs(t, producer), "3", "3", "0", "0")
	})

	t.Run("forced re-sync of an unchanged head", func(t *testing.T) {
		svc, producer := newService(func(string, string) (*github.Comparison, error) {
			return nil, errors.New("an unchanged head needs no diff")
		})
		source := githubSource(t, svc, "main", []string{"docs"}, "HEAD")
		if _, err := svc.ReconcileGitHubSource(context.Background(), source, true, "11111111-1111-1111-1111-111111111111"); err != nil {
			t.Fatal(err)
		}
		wantCounts(t, snapshotAttrs(t, producer), "3", "0", "0", "0")
	})

	t.Run("moved head", func(t *testing.T) {
		svc, producer := newService(func(base, head string) (*github.Comparison, error) {
			if base != "OLD" || head != "HEAD" {
				t.Fatalf("diffed %s...%s, want OLD...HEAD", base, head)
			}
			return &github.Comparison{Status: github.CompareStatusAhead, Files: []github.ChangedFile{
				{Filename: "docs/c.md", Status: "added", SHA: "sc"},
				{Filename: "docs/a.md", Status: "modified", SHA: "sa"},
				{Filename: "docs/gone.md", Status: "removed"},
				{Filename: "src/main.go", Status: "modified", SHA: "sx"},
			}}, nil
		})
		source := githubSource(t, svc, "main", []string{"docs"}, "OLD")
		if _, err := svc.ReconcileGitHubSource(context.Background(), source, true, "11111111-1111-1111-1111-111111111111"); err != nil {
			t.Fatal(err)
		}
		wantCounts(t, snapshotAttrs(t, producer), "3", "1", "1", "1")
	})

	t.Run("previous commit gone", func(t *testing.T) {
		svc, producer := newService(func(string, string) (*github.Comparison, error) {
			return nil, github.ErrNotFound
		})
		source := githubSource(t, svc, "main", []string{"docs"}, "OLD")
		if _, err := svc.ReconcileGitHubSource(context.Background(), source, true, "11111111-1111-1111-1111-111111111111"); err != nil {
			t.Fatal(err)
		}
		attrs := snapshotAttrs(t, producer)
		if attrs["datasource.changes.files"] != "3" || attrs["datasource.changes.split_known"] != "false" {
			t.Fatalf("a snapshot with no diff must report its file count and an unknown split, got %v", attrs)
		}
		if _, ok := attrs["datasource.changes.added"]; ok {
			t.Fatalf("an unknown split must not report counts that read as zero: %v", attrs)
		}
	})
}

func at(minute int) *time.Time {
	t := time.Date(2026, 9, 26, 12, minute, 0, 0, time.UTC)
	return &t
}

func TestProjectDatasourceSyncProgress(t *testing.T) {
	queued := business.DatasourceSyncRecord{JobID: "j", Topic: business.DatasourceReconcileTopic, ReconcileMode: "force", CreatedAt: *at(0), MaxAttempts: 24}
	snapshot := business.DatasourceSyncHandoff{Jobs: 1, FirstAt: at(2), Snapshot: true, Commit: "c1",
		SnapshotFiles: "42", SnapshotAdded: "42", SnapshotModified: "0", SnapshotDeleted: "0", SnapshotSplitKnown: "true"}

	for _, tc := range []struct {
		name    string
		record  func(r *business.DatasourceSyncRecord)
		handoff business.DatasourceSyncHandoff
		check   func(t *testing.T, p business.DatasourceSyncProgress)
	}{
		{"queued", func(*business.DatasourceSyncRecord) {}, business.DatasourceSyncHandoff{}, func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseQueued || p.Trigger != business.DatasourceSyncTriggerManual || !p.QueuedAt.Equal(*at(0)) || p.Changes != nil || p.Failure != nil {
				t.Fatalf("%+v", p)
			}
		}},
		{"fetching", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.LastAttemptAt = jobsv1.JobState_JOB_STATE_PROCESSING, 1, at(1)
		}, business.DatasourceSyncHandoff{}, func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseFetching || !p.FetchingAt.Equal(*at(1)) || p.Attempt != 1 || p.MaxAttempts != 24 {
				t.Fatalf("%+v", p)
			}
		}},
		{"compiled", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.LastAttemptAt = jobsv1.JobState_JOB_STATE_PROCESSING, 1, at(1)
		}, snapshot, func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseCompiled || !p.CompiledAt.Equal(*at(2)) || p.HandedOffAt != nil {
				t.Fatalf("%+v", p)
			}
			if c := p.Changes; c == nil || c.Files != 42 || c.Added != 42 || !c.SplitKnown || !c.Snapshot || c.Commit != "c1" {
				t.Fatalf("changes %+v", p.Changes)
			}
		}},
		{"handed off, not yet taken", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.LastAttemptAt, r.CompletedAt = jobsv1.JobState_JOB_STATE_SUCCEEDED, 1, at(1), at(3)
		}, snapshot, func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseHandedOff || !p.HandedOffAt.Equal(*at(3)) || p.FinishedAt != nil {
				t.Fatalf("%+v", p)
			}
		}},
		{"done once the module took the change set", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.LastAttemptAt, r.CompletedAt = jobsv1.JobState_JOB_STATE_SUCCEEDED, 1, at(1), at(3)
		}, func() business.DatasourceSyncHandoff {
			h := snapshot
			h.Succeeded, h.LastDoneAt = 1, at(5)
			return h
		}(), func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseDone || !p.HandedOffAt.Equal(*at(3)) || !p.FinishedAt.Equal(*at(5)) {
				t.Fatalf("%+v", p)
			}
		}},
		{"done with nothing to hand off", func(r *business.DatasourceSyncRecord) {
			r.ReconcileMode = "conditional"
			r.State, r.Attempt, r.LastAttemptAt, r.CompletedAt = jobsv1.JobState_JOB_STATE_SUCCEEDED, 1, at(1), at(1)
		}, business.DatasourceSyncHandoff{}, func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseDone || p.Changes != nil || !p.FinishedAt.Equal(*at(1)) || p.Trigger != business.DatasourceSyncTriggerScheduled {
				t.Fatalf("%+v", p)
			}
		}},
		{"rate limited, waiting for the reset", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.LastAttemptAt, r.AvailableAt = jobsv1.JobState_JOB_STATE_RETRYING, 1, at(1), at(40)
			r.ErrorCode, r.ErrorMessage = "datasource.github_unauthenticated_rate_limited", "GitHub rate limited the request."
		}, business.DatasourceSyncHandoff{}, func(t *testing.T, p business.DatasourceSyncProgress) {
			f := p.Failure
			if p.Phase != business.DatasourceSyncPhaseQueued || f == nil || f.Reason != business.DatasourceSyncFailureRateLimited ||
				!f.Retrying || !f.RetryAt.Equal(*at(40)) || f.Message == "" {
				t.Fatalf("%+v / %+v", p, f)
			}
		}},
		{"failed for good", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.DeadAt, r.AvailableAt = jobsv1.JobState_JOB_STATE_DEAD_LETTER, 24, at(9), at(9)
			r.ErrorCode, r.ErrorMessage = "datasource.github_not_found", "not found"
		}, business.DatasourceSyncHandoff{}, func(t *testing.T, p business.DatasourceSyncProgress) {
			f := p.Failure
			if p.Phase != business.DatasourceSyncPhaseFailed || !p.FinishedAt.Equal(*at(9)) || f == nil ||
				f.Reason != business.DatasourceSyncFailureNotFound || f.Retrying || f.RetryAt != nil {
				t.Fatalf("%+v / %+v", p, f)
			}
		}},
		{"the module dead-lettered the change set", func(r *business.DatasourceSyncRecord) {
			r.State, r.Attempt, r.CompletedAt = jobsv1.JobState_JOB_STATE_SUCCEEDED, 1, at(3)
		}, func() business.DatasourceSyncHandoff {
			h := snapshot
			h.Dead, h.FirstDeadAt = 1, at(7)
			return h
		}(), func(t *testing.T, p business.DatasourceSyncProgress) {
			if p.Phase != business.DatasourceSyncPhaseFailed || p.Failure == nil || p.Failure.Reason != business.DatasourceSyncFailureDeliveryFailed || !p.FinishedAt.Equal(*at(7)) {
				t.Fatalf("%+v", p)
			}
		}},
		{"an incremental change set counts its files by change type", func(r *business.DatasourceSyncRecord) {
			r.Topic, r.ReconcileMode = "datasource.github.push", ""
			r.State, r.Attempt, r.CompletedAt = jobsv1.JobState_JOB_STATE_SUCCEEDED, 1, at(3)
		}, business.DatasourceSyncHandoff{Jobs: 4, Succeeded: 4, FirstAt: at(2), LastDoneAt: at(4), Added: 1, Modified: 2, Deleted: 1, Commit: "c2"},
			func(t *testing.T, p business.DatasourceSyncProgress) {
				c := p.Changes
				if p.Trigger != business.DatasourceSyncTriggerWebhook || c == nil || c.Snapshot || c.Files != 4 || c.Added != 1 || c.Modified != 2 || c.Deleted != 1 || !c.SplitKnown {
					t.Fatalf("%+v / %+v", p, c)
				}
			}},
		{"a snapshot handed off without counts reports none", func(r *business.DatasourceSyncRecord) {
			r.State, r.CompletedAt = jobsv1.JobState_JOB_STATE_SUCCEEDED, at(3)
		}, business.DatasourceSyncHandoff{Jobs: 1, FirstAt: at(2), Snapshot: true}, func(t *testing.T, p business.DatasourceSyncProgress) {
			if c := p.Changes; c == nil || !c.Snapshot || c.Files != 0 || c.SplitKnown {
				t.Fatalf("%+v", p.Changes)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := queued
			record.State = jobsv1.JobState_JOB_STATE_PENDING
			tc.record(&record)
			tc.check(t, business.ProjectDatasourceSyncProgress(record, tc.handoff))
		})
	}
}

func TestDatasourceSyncFailureReasonForCode(t *testing.T) {
	for code, want := range map[string]business.DatasourceSyncFailureReason{
		"datasource.github_rate_limited":                 business.DatasourceSyncFailureRateLimited,
		"datasource.github_unauthenticated_rate_limited": business.DatasourceSyncFailureRateLimited,
		"datasource.github_unauthorized":                 business.DatasourceSyncFailureCredential,
		"datasource.credential_unreadable":               business.DatasourceSyncFailureCredential,
		"datasource.github_access_or_rate_limit":         business.DatasourceSyncFailureAccessDenied,
		"datasource.github_not_found":                    business.DatasourceSyncFailureNotFound,
		"datasource.repository_too_large":                business.DatasourceSyncFailureTooLarge,
		"datasource.too_many_files":                      business.DatasourceSyncFailureTooLarge,
		"datasource.git_unavailable":                     business.DatasourceSyncFailureHostUnavailable,
		"jobs.handler_failed":                            business.DatasourceSyncFailureOther,
	} {
		if got := business.DatasourceSyncFailureReasonForCode(code); got != want {
			t.Errorf("%s = %v, want %v", code, got, want)
		}
	}
}

// A per-source webhook delivery is enqueued by the receiver in pkg/datasource;
// the progress read must name the same topic and source or a webhook sync
// would never appear as the source's latest.
func TestSyncProgressReadsTheReceiversWebhookJobs(t *testing.T) {
	if !slices.Contains(business.DatasourceSyncSources, datasource.GitHubWebhookSource) {
		t.Fatalf("sync sources %v miss the receiver's %q", business.DatasourceSyncSources, datasource.GitHubWebhookSource)
	}
	if !slices.Contains(business.DatasourceSyncTopics, datasource.GitHubWebhookTopic) {
		t.Fatalf("sync topics %v miss the receiver's %q", business.DatasourceSyncTopics, datasource.GitHubWebhookTopic)
	}
	if datasource.GitHubWebhookQueue != business.DatasourceDeliveryQueue {
		t.Fatalf("receiver queue %q is not the delivery queue %q", datasource.GitHubWebhookQueue, business.DatasourceDeliveryQueue)
	}
}
