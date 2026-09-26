package connector_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"accounts/pkg/datasource/connector"
)

type recordingScheduler struct {
	acquired []connector.Priority
	refuse   error
	blocked  []time.Time
}

func (r *recordingScheduler) Acquire(_ context.Context, _ connector.Source, _ connector.Budget, p connector.Priority) error {
	r.acquired = append(r.acquired, p)
	return r.refuse
}

func (r *recordingScheduler) Blocked(_ context.Context, _ connector.Source, limited *connector.RateLimitedError) error {
	r.blocked = append(r.blocked, limited.ResetAt)
	return nil
}

type scriptedFiles struct {
	stubFiles
	calls int
	err   error
}

func (s *scriptedFiles) Changes(context.Context, connector.Source, string) (connector.ChangeSet, error) {
	s.calls++
	return connector.ChangeSet{}, s.err
}

func (s *scriptedFiles) FetchFiles(context.Context, connector.Source, string, []connector.FileRef, func(connector.File, io.Reader) error) error {
	s.calls++
	return s.err
}

func (s *scriptedFiles) Version(context.Context, connector.Source) (string, error) {
	s.calls++
	return "v", s.err
}

func TestBudgetedMetersEveryOperationAtItsPriority(t *testing.T) {
	inner := &scriptedFiles{stubFiles: stubFiles{good("example")}}
	sched := &recordingScheduler{}
	c := connector.Budgeted(inner, sched)
	ctx := context.Background()
	if _, err := c.Version(ctx, connector.Source{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Changes(connector.WithPriority(ctx, connector.PriorityInteractive), connector.Source{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.FetchFiles(ctx, connector.Source{}, "v", nil, nil); err != nil {
		t.Fatal(err)
	}
	want := []connector.Priority{connector.PriorityBackground, connector.PriorityInteractive, connector.PriorityBackground}
	if len(sched.acquired) != 3 || sched.acquired[0] != want[0] || sched.acquired[1] != want[1] || sched.acquired[2] != want[2] {
		t.Fatalf("acquired %v, want %v: unmarked work is background", sched.acquired, want)
	}
	if c.Descriptor().Key != "example" {
		t.Fatal("the descriptor passes through")
	}
}

func TestBudgetedRefusalNeverReachesTheProvider(t *testing.T) {
	inner := &scriptedFiles{stubFiles: stubFiles{good("example")}}
	refusal := &connector.RateLimitedError{ResetAt: time.Now().Add(time.Hour), Scope: "credential", Yielded: true}
	sched := &recordingScheduler{refuse: refusal}
	_, err := connector.Budgeted(inner, sched).Changes(context.Background(), connector.Source{}, "")
	if !errors.Is(err, refusal) || inner.calls != 0 {
		t.Fatalf("err = %v after %d provider calls; the budget refuses before the provider is asked", err, inner.calls)
	}
	if len(sched.blocked) != 0 {
		t.Fatal("the host's own refusal is not a provider block")
	}
}

func TestBudgetedRecordsAProviderRateLimit(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute)
	inner := &scriptedFiles{stubFiles: stubFiles{good("example")}, err: &connector.RateLimitedError{ResetAt: reset, Scope: "credential"}}
	sched := &recordingScheduler{}
	_, err := connector.Budgeted(inner, sched).Changes(context.Background(), connector.Source{}, "")
	var limited *connector.RateLimitedError
	if !errors.As(err, &limited) || len(sched.blocked) != 1 || !sched.blocked[0].Equal(reset) {
		t.Fatalf("err = %v, blocked = %v: a provider rate limit blocks the credential until it resets", err, sched.blocked)
	}
}

func TestPriorityDefaultsToBackground(t *testing.T) {
	if connector.PriorityFrom(context.Background()) != connector.PriorityBackground {
		t.Fatal("unmarked work must be background, so it yields")
	}
}
