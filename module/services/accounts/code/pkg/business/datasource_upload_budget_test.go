package business_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// recordingBudgets is a meter that always admits and records what was spent:
// under which credential key, and against which limit — the whole quota for an
// interactive operation, the background share otherwise.
type recordingBudgets struct {
	mu     sync.Mutex
	spends []budgetSpend
}

type budgetSpend struct {
	key   string
	limit int
}

func (r *recordingBudgets) SpendDatasourceBudget(_ context.Context, key string, limit int, window time.Duration, now time.Time) (bool, time.Time, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spends = append(r.spends, budgetSpend{key, limit})
	return true, now.Add(window), time.Time{}, nil
}

func (r *recordingBudgets) BlockDatasourceBudget(context.Context, string, time.Time) error {
	return nil
}

func (r *recordingBudgets) take() []budgetSpend {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.spends
	r.spends = nil
	return out
}

// TestUploadSourcesOnOneCredentialSpendOneBudget holds object storage to the
// scheduler's contract: one provider credential's quota is shared by all its
// sources. Two sources reading with the same access key at the same store —
// two audiences of one bucket, which the descriptor recommends — spend one
// budget; a third with another key spends its own. And a sync a person asked
// for may spend the whole quota, while the scheduled pass spends only the
// background share.
func TestUploadSourcesOnOneCredentialSpendOneBudget(t *testing.T) {
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), producer, nil)
	svc.SetDatasourceKeys(nil, []byte("a-delivered-link-key"))
	budgets := &recordingBudgets{}
	svc.SetDatasourceBudgetStore(budgets)
	useUploadBucket(svc, &fakeUploadBucket{objects: []fakeObject{{key: "kb/a.md", etag: "a", body: "A"}}})
	add := func(prefix, keyID string) *business.DatasourceSource {
		cfg := uploadConfig()
		cfg.Prefix, cfg.AccessKeyID = prefix, keyID
		source, err := svc.AddSource(context.Background(), "actor-1", business.AddSourceInput{
			OrgID: testOrg, Provider: business.DatasourceProviderUpload, CollectionLabel: "guides-" + prefix + keyID, Credential: "secretkey", Upload: cfg,
		})
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	keyOf := func(source *business.DatasourceSource) string {
		budgets.take()
		if _, err := svc.RunDatasourceSync(context.Background(), source.ID); err != nil {
			t.Fatal(err)
		}
		spends := budgets.take()
		if len(spends) == 0 {
			t.Fatalf("a sync of %s spent no budget", source.ID)
		}
		for _, s := range spends[1:] {
			if s.key != spends[0].key {
				t.Fatalf("one sync spent under two keys: %v", spends)
			}
		}
		return spends[0].key
	}
	staff, interns, other := add("kb/", "AKIASHARED"), add("kb/interns/", "AKIASHARED"), add("kb/", "AKIAOTHER")
	if a, b := keyOf(staff), keyOf(interns); a != b || a == "" {
		t.Fatalf("two sources on one credential spent under %q and %q, want one key", a, b)
	}
	if keyOf(staff) == keyOf(other) {
		t.Fatal("sources on different credentials share a budget")
	}
	for _, s := range []*business.DatasourceSource{staff, interns, other} {
		if s.Upload.CredentialBudgetKey == "" || s.Upload.CredentialBudgetKey == "AKIASHARED" {
			t.Fatalf("source %s stores budget key %q, want a keyed digest", s.ID, s.Upload.CredentialBudgetKey)
		}
	}

	handler := svc.NewDatasourceSyncJobHandler()
	if _, err := svc.SyncDatasourceSource(context.Background(), "actor-1", testOrg, staff.ID); err != nil {
		t.Fatal(err)
	}
	requested := producer.jobs[len(producer.jobs)-1]
	limitOf := func(source string) int {
		budgets.take()
		envelope := &jobsv1.JobEnvelope{Queue: requested.GetQueue(), Topic: requested.GetTopic(), Source: source, Attributes: requested.GetAttributes()}
		if err := handler(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
		spends := budgets.take()
		if len(spends) == 0 {
			t.Fatal("the sync spent no budget")
		}
		return spends[0].limit
	}
	interactive, background := limitOf(requested.GetSource()), limitOf(business.DatasourceScheduledSyncSource)
	if interactive <= background {
		t.Fatalf("a requested sync spent against %d and the scheduled pass against %d, want the requested one to reach the reserved share", interactive, background)
	}
}
