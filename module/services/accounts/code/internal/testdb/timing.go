package testdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/codefly-dev/core/sdk"
)

// Measure reports setup and execution separately even when setup fails before
// testing.M can emit any test events.
func Measure(target, phase string, dependencies []string, budget time.Duration) func(bool) {
	return measure(os.Stderr, target, phase, dependencies, budget)
}

// MeasureSetup records the dependency-setup phase from the error the SDK
// returned. Dependency names describe the requested graph; when the SDK
// attributes the failure — a readiness timeout names the services still
// starting, a readiness failure the ones that could not start — the record
// names those services too, so an overrun is charged to the dependency that
// caused it rather than to the graph as a whole.
func MeasureSetup(target string, dependencies []string, budget time.Duration) func(error) {
	return measureSetup(os.Stderr, target, dependencies, budget)
}

type timingRecord struct {
	Target       string   `json:"target"`
	Phase        string   `json:"phase"`
	Dependencies []string `json:"dependencies,omitempty"`
	ElapsedMS    int64    `json:"elapsed_ms"`
	BudgetMS     int64    `json:"budget_ms,omitempty"`
	Failed       bool     `json:"failed"`
	// Pending names the services that had not become ready when the budget
	// ran out.
	Pending []string `json:"pending,omitempty"`
	// FailedToStart names the services the flow reported it cannot start.
	FailedToStart []string `json:"failed_to_start,omitempty"`
}

func measure(out io.Writer, target, phase string, dependencies []string, budget time.Duration) func(bool) {
	start := time.Now()
	return func(failed bool) {
		emit(out, timingRecord{
			Target: target, Phase: phase, Dependencies: dependencies,
			ElapsedMS: time.Since(start).Milliseconds(), BudgetMS: budget.Milliseconds(), Failed: failed,
		})
	}
}

func measureSetup(out io.Writer, target string, dependencies []string, budget time.Duration) func(error) {
	start := time.Now()
	return func(err error) {
		record := timingRecord{
			Target: target, Phase: "dependency-setup", Dependencies: dependencies,
			ElapsedMS: time.Since(start).Milliseconds(), BudgetMS: budget.Milliseconds(), Failed: err != nil,
		}
		var timeout *sdk.ReadinessTimeout
		if errors.As(err, &timeout) {
			for _, service := range timeout.Pending() {
				record.Pending = append(record.Pending, service.GetService())
			}
		}
		var failure *sdk.ReadinessFailure
		if errors.As(err, &failure) {
			for _, service := range failure.Services {
				record.FailedToStart = append(record.FailedToStart, service.GetService())
			}
		}
		emit(out, record)
	}
}

func emit(out io.Writer, record timingRecord) {
	data, _ := json.Marshal(record)
	_, _ = fmt.Fprintln(out, string(data))
}
