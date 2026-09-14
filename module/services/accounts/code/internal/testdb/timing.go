package testdb

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Measure reports setup and execution separately even when setup fails before
// testing.M can emit any test events. Dependency names describe the requested
// graph passed to this wrapper. Core can now carry per-service readiness, but
// this timing record receives no status snapshot and must not infer a culprit.
func Measure(target, phase string, dependencies []string, budget time.Duration) func(bool) {
	return measure(os.Stderr, target, phase, dependencies, budget)
}

func measure(out io.Writer, target, phase string, dependencies []string, budget time.Duration) func(bool) {
	start := time.Now()
	return func(failed bool) {
		record := struct {
			Target       string   `json:"target"`
			Phase        string   `json:"phase"`
			Dependencies []string `json:"dependencies,omitempty"`
			ElapsedMS    int64    `json:"elapsed_ms"`
			BudgetMS     int64    `json:"budget_ms,omitempty"`
			Failed       bool     `json:"failed"`
		}{target, phase, dependencies, time.Since(start).Milliseconds(), budget.Milliseconds(), failed}
		data, _ := json.Marshal(record)
		_, _ = fmt.Fprintln(out, string(data))
	}
}
