package testdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	cliv0 "github.com/codefly-dev/core/generated/go/codefly/cli/v0"
	"github.com/codefly-dev/core/sdk"
)

// Measure reports setup and execution separately even when setup fails before
// testing.M can emit any test events. Pass the SDK setup error to attribute
// startup failures to the dependencies and lifecycle stages it reported.
func Measure(target, phase string, dependencies []string, budget time.Duration) func(bool, error) {
	return measure(os.Stderr, target, phase, dependencies, budget)
}

func measure(out io.Writer, target, phase string, dependencies []string, budget time.Duration) func(bool, error) {
	start := time.Now()
	return func(failed bool, setupErr error) {
		var timeout *sdk.ReadinessTimeout
		var failure *sdk.ReadinessFailure
		var services []*cliv0.ServiceReadiness
		switch {
		case errors.As(setupErr, &timeout):
			services = timeout.Pending()
		case errors.As(setupErr, &failure):
			services = failure.Services
		}
		type blockedService struct {
			Service   string `json:"service"`
			Lifecycle string `json:"lifecycle"`
		}
		blocked := make([]blockedService, 0, len(services))
		for _, service := range services {
			blocked = append(blocked, blockedService{service.GetService(), service.GetLifecycle().String()})
		}
		record := struct {
			Target       string           `json:"target"`
			Phase        string           `json:"phase"`
			Dependencies []string         `json:"dependencies,omitempty"`
			ElapsedMS    int64            `json:"elapsed_ms"`
			BudgetMS     int64            `json:"budget_ms,omitempty"`
			Failed       bool             `json:"failed"`
			Blocked      []blockedService `json:"blocked_services,omitempty"`
		}{target, phase, dependencies, time.Since(start).Milliseconds(), budget.Milliseconds(), failed, blocked}
		data, _ := json.Marshal(record)
		_, _ = fmt.Fprintln(out, string(data))
	}
}
