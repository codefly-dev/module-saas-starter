package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
)

func TestSnapshotCarriesResolvedSourceBinding(t *testing.T) {
	for _, branch := range []string{"", "release/docs"} {
		for _, trigger := range []string{"reconcile", "periodic", "created-push", "force-push", "missing-base", "truncated-compare"} {
			t.Run(trigger+"/"+branch, func(t *testing.T) {
				producer := &recordingProducer{}
				client := &fakeGitHub{
					defaultBranch: "trunk", commit: "head",
					files: []github.File{{Path: "docs/readme.md", SHA: "blob", Size: 3}},
					compareFn: func(string, string) (*github.Comparison, error) {
						if trigger == "missing-base" {
							return nil, github.ErrNotFound
						}
						if trigger == "truncated-compare" {
							return &github.Comparison{Status: github.CompareStatusAhead, Truncated: true}, nil
						}
						return &github.Comparison{Status: github.CompareStatusDiverged}, nil
					},
				}
				svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, client)
				source := githubSource(t, svc, branch, nil, "")
				resolved := branch
				if resolved == "" {
					resolved = "trunk"
				}
				want := "refs/heads/" + resolved
				var err error
				if trigger == "reconcile" || trigger == "periodic" {
					_, err = svc.ReconcileGitHubSource(context.Background(), source, trigger == "reconcile", "sync-request")
				} else {
					if trigger == "force-push" || trigger == "missing-base" {
						source.LastIngestedCommit = "old"
					}
					_, err = svc.CompileGitHubDelivery(context.Background(), source, pushDelivery(want, "old", "head", trigger == "created-push", false), "delivery")
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(producer.jobs) != 1 {
					t.Fatalf("jobs = %d, want one snapshot", len(producer.jobs))
				}
				job := producer.jobs[0]
				if job.GetTopic() != business.DatasourceSnapshotTopic {
					t.Fatalf("not a snapshot: %s", job.GetTopic())
				}
				if got := job.GetAttributes()["datasource.source_ref"]; got != want {
					t.Fatalf("source binding = %q, want %q", got, want)
				}
				if got := decodeChangeSetFile(t, job)["ref"]; got != want {
					t.Fatalf("manifest ref = %v, want %q", got, want)
				}
			})
		}
	}
}
