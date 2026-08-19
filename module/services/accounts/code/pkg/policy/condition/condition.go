// Package condition evaluates the bounded attribute predicates declared beside
// an RPC via saas.policy.v1.MethodPolicy.conditions. The vocabulary is a closed
// enum, not a policy language: every attribute maps to one hand-written Go
// evaluator, and an unrecognized or malformed condition denies. Predicates are
// meant to compose above the forced RLS floor, never to replace it.
package condition

import (
	"fmt"
	"time"

	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

const minutesPerDay = 24 * 60

// Input is the closed set of caller and record facts the predicates read. The
// decision path populates it from resolved request state; predicates never
// reach outside it.
type Input struct {
	CallerTeamIDs        []string
	RecordOwnerTeamID    string
	RecordStatus         string
	RecordClassification int32
	CallerClearance      int32
	Now                  time.Time
}

// EvaluateAll reports whether every condition holds for the input (logical
// AND). Zero conditions is allowed; any unrecognized or malformed condition, or
// any predicate that does not hold, denies. The error is non-nil only when a
// condition cannot be evaluated at all — a fail-closed signal distinct from a
// well-formed predicate that simply evaluates to false.
func EvaluateAll(conditions []*policyv1.Condition, in Input) (bool, error) {
	for _, c := range conditions {
		ok, err := evaluate(c, in)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evaluate(c *policyv1.Condition, in Input) (bool, error) {
	if err := validate(c); err != nil {
		return false, err
	}
	switch c.GetAttribute() {
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_OWNER_TEAM:
		return callerOwnsTeam(in), nil
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_STATUS:
		return contains(c.GetAllowedStatuses(), in.RecordStatus), nil
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_CLASSIFICATION:
		return in.CallerClearance >= in.RecordClassification, nil
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_TIME_WINDOW:
		return withinWindow(c.GetTimeWindow(), in.Now)
	default:
		return false, fmt.Errorf("unknown condition attribute %v", c.GetAttribute())
	}
}

func callerOwnsTeam(in Input) bool {
	if in.RecordOwnerTeamID == "" {
		return false
	}
	return contains(in.CallerTeamIDs, in.RecordOwnerTeamID)
}

func withinWindow(w *policyv1.TimeWindow, now time.Time) (bool, error) {
	loc, err := time.LoadLocation(w.GetTimezone())
	if err != nil {
		return false, fmt.Errorf("invalid time-window timezone %q: %w", w.GetTimezone(), err)
	}
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	return minute >= int(w.GetStartMinute()) && minute < int(w.GetEndMinute()), nil
}

// Validate reports whether a set of declared conditions is well-formed, without
// evaluating them against any input. The catalog compiler calls it so an
// unknown attribute or malformed parameters fail the build closed rather than
// reaching runtime.
func Validate(conditions []*policyv1.Condition) error {
	for _, c := range conditions {
		if err := validate(c); err != nil {
			return err
		}
	}
	return nil
}

func validate(c *policyv1.Condition) error {
	if c == nil {
		return fmt.Errorf("condition is missing")
	}
	hasStatuses := len(c.GetAllowedStatuses()) > 0
	hasWindow := c.GetTimeWindow() != nil
	switch c.GetAttribute() {
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_OWNER_TEAM,
		policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_CLASSIFICATION:
		if hasStatuses || hasWindow {
			return fmt.Errorf("condition %v takes no parameters", c.GetAttribute())
		}
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_STATUS:
		if hasWindow {
			return fmt.Errorf("status condition sets a time window")
		}
		if !hasStatuses {
			return fmt.Errorf("status condition allows no statuses")
		}
		for _, s := range c.GetAllowedStatuses() {
			if s == "" {
				return fmt.Errorf("status condition has an empty status")
			}
		}
	case policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_TIME_WINDOW:
		if hasStatuses {
			return fmt.Errorf("time-window condition sets allowed statuses")
		}
		if err := validateWindow(c.GetTimeWindow()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown condition attribute %v", c.GetAttribute())
	}
	return nil
}

func validateWindow(w *policyv1.TimeWindow) error {
	if w == nil {
		return fmt.Errorf("time-window condition has no window")
	}
	if w.GetEndMinute() > minutesPerDay {
		return fmt.Errorf("time-window end %d exceeds %d", w.GetEndMinute(), minutesPerDay)
	}
	if w.GetStartMinute() >= w.GetEndMinute() {
		return fmt.Errorf("time-window start %d is not before end %d", w.GetStartMinute(), w.GetEndMinute())
	}
	if _, err := time.LoadLocation(w.GetTimezone()); err != nil {
		return fmt.Errorf("invalid time-window timezone %q: %w", w.GetTimezone(), err)
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
