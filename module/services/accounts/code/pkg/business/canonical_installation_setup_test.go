package business_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

type canonicalSetupStore struct {
	business.Store
	attempts []domain.Installation
	err      error
}

func (s *canonicalSetupStore) EnqueueCanonicalInstallationStep(_ context.Context, inst domain.Installation, _ bool) (string, error) {
	s.attempts = append(s.attempts, inst)
	return "setup-job", s.err
}

func (*canonicalSetupStore) SolutionInstallationPlan(context.Context, string, string) (business.SolutionInstallationPlan, error) {
	return business.SolutionInstallationPlan{}, errors.New("not a claim test")
}

type canonicalSetupResults struct {
	jobs.Operations
	get func(context.Context, string) (*jobsv1.GetJobResponse, error)
}

func (s canonicalSetupResults) GetJob(ctx context.Context, req *jobsv1.GetJobRequest) (*jobsv1.GetJobResponse, error) {
	return s.get(ctx, req.GetJobId())
}

func setupResult(state jobsv1.JobState) *jobsv1.GetJobResponse {
	return &jobsv1.GetJobResponse{Job: &jobsv1.JobSummary{
		Id: "setup-job", Queue: business.InstallationStepQueue, Source: "saas.installation", Topic: "installation.step",
		Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: "org"}}, State: state,
		LastFailure: &jobsv1.JobFailure{Message: "required setup refused"},
	}}
}

func TestCanonicalSetupAcknowledgesOnlyItsDurableSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *jobsv1.GetJobResponse
		err    error
		want   string
	}{
		{"success", setupResult(jobsv1.JobState_JOB_STATE_SUCCEEDED), nil, ""},
		{"terminal refusal", setupResult(jobsv1.JobState_JOB_STATE_DEAD_LETTER), nil, "INSTALLATION_SETUP_FAILED"},
		{"superseded", setupResult(jobsv1.JobState_JOB_STATE_CANCELED), nil, "INSTALLATION_SETUP_CHANGED"},
		{"read outage", nil, errors.New("queue unavailable"), "queue unavailable"},
		{"missing result", nil, nil, "INSTALLATION_SETUP_CHANGED"},
		{"foreign job", &jobsv1.GetJobResponse{Job: &jobsv1.JobSummary{Id: "other", State: jobsv1.JobState_JOB_STATE_SUCCEEDED}}, nil, "INSTALLATION_SETUP_CHANGED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &canonicalSetupStore{}
			s, err := business.NewService(store)
			if err != nil {
				t.Fatal(err)
			}
			s.SetJobOperations(canonicalSetupResults{get: func(_ context.Context, id string) (*jobsv1.GetJobResponse, error) {
				if id != "setup-job" {
					t.Fatalf("read another job: %q", id)
				}
				return tc.result, tc.err
			}})
			inst := domain.Installation{ID: "installation", Organization: "org", Revision: 4, Status: domain.Pending,
				Consent: domain.ConsentReceipt{OperationID: "original-operation", Intent: "original-intent", Revision: 4}}
			for range 2 {
				err = s.AuthorityInstallStep()(context.Background(), inst)
				if tc.want == "" && err != nil {
					t.Fatal(err)
				}
				if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			}
			for _, attempt := range store.attempts {
				if attempt.ID != inst.ID || attempt.Revision != inst.Revision || attempt.Consent != inst.Consent || attempt.Status != domain.Pending {
					t.Fatal("retry changed the canonical attempt or activated it")
				}
			}
		})
	}
}

func TestCanonicalSetupPendingWaitIsBoundedAndResumesSameAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &canonicalSetupStore{}
		s, err := business.NewService(store)
		if err != nil {
			t.Fatal(err)
		}
		state := jobsv1.JobState_JOB_STATE_PROCESSING
		s.SetJobOperations(canonicalSetupResults{get: func(context.Context, string) (*jobsv1.GetJobResponse, error) {
			return setupResult(state), nil
		}})
		inst := domain.Installation{ID: "installation", Organization: "org", Revision: 7, Status: domain.Pending}
		start := time.Now()
		err = s.AuthorityInstallStep()(context.Background(), inst)
		if err == nil || !strings.Contains(err.Error(), "INSTALLATION_SETUP_PENDING") {
			t.Fatalf("pending became an ACK: %v", err)
		}
		if time.Since(start) != business.InstallationStepTimeout {
			t.Fatal("setup wait did not obey its bound")
		}
		state = jobsv1.JobState_JOB_STATE_SUCCEEDED
		if err := s.AuthorityInstallStep()(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
		if len(store.attempts) != 2 || store.attempts[0].ID != store.attempts[1].ID || store.attempts[0].Revision != store.attempts[1].Revision {
			t.Fatal("resume replaced the receipt/revision")
		}
	})
}

// A read that the bounded wait ends is the wait's end, not the result
// unavailable: the setup is reported pending, with the bound it outlived.
func TestCanonicalSetupWaitEndingInsideTheReadIsPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, err := business.NewService(&canonicalSetupStore{})
		if err != nil {
			t.Fatal(err)
		}
		s.SetJobOperations(canonicalSetupResults{get: func(ctx context.Context, _ string) (*jobsv1.GetJobResponse, error) {
			<-ctx.Done()
			return nil, fmt.Errorf("get job: %w", ctx.Err())
		}})
		start := time.Now()
		err = s.AuthorityInstallStep()(context.Background(), domain.Installation{ID: "installation", Organization: "org", Revision: 7, Status: domain.Pending})
		if err == nil || !strings.Contains(err.Error(), "INSTALLATION_SETUP_PENDING") || !strings.Contains(err.Error(), business.InstallationStepTimeout.String()) {
			t.Fatalf("a wait ending inside the read is not reported pending with its bound: %v", err)
		}
		if time.Since(start) != business.InstallationStepTimeout {
			t.Fatal("setup wait did not obey its bound")
		}
	})
}

// Inside a delivery pass (WithoutSetupWait) a step never waits on a setup:
// one read decides, a setup still running is pending at once, and the next
// pass resumes the same attempt and activates it once the setup succeeded.
func TestCanonicalSetupInsideAPassNeverWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &canonicalSetupStore{}
		s, err := business.NewService(store)
		if err != nil {
			t.Fatal(err)
		}
		state := jobsv1.JobState_JOB_STATE_PROCESSING
		s.SetJobOperations(canonicalSetupResults{get: func(context.Context, string) (*jobsv1.GetJobResponse, error) {
			return setupResult(state), nil
		}})
		inst := domain.Installation{ID: "installation", Organization: "org", Revision: 7, Status: domain.Pending}
		ctx := business.WithoutSetupWait(context.Background())
		start := time.Now()
		err = s.AuthorityInstallStep()(ctx, inst)
		if err == nil || !strings.Contains(err.Error(), "INSTALLATION_SETUP_PENDING") {
			t.Fatalf("a running setup is not pending: %v", err)
		}
		if waited := time.Since(start); waited != 0 {
			t.Fatalf("the step waited %s inside a pass", waited)
		}
		state = jobsv1.JobState_JOB_STATE_SUCCEEDED
		if err := s.AuthorityInstallStep()(ctx, inst); err != nil {
			t.Fatalf("the next pass does not resume the succeeded attempt: %v", err)
		}
		if len(store.attempts) != 2 || store.attempts[0].ID != store.attempts[1].ID || store.attempts[0].Revision != store.attempts[1].Revision {
			t.Fatal("the resume replaced the attempt")
		}
		state = jobsv1.JobState_JOB_STATE_DEAD_LETTER
		if err := s.AuthorityInstallStep()(ctx, inst); err == nil || !strings.Contains(err.Error(), "INSTALLATION_SETUP_FAILED") {
			t.Fatalf("a failed setup is not said: %v", err)
		}
	})
}
