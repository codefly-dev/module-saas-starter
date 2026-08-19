package condition

import (
	"testing"
	"time"

	policyv1 "accounts/pkg/gen/saas/policy/v1"

	"github.com/stretchr/testify/require"
)

func ownerTeam() *policyv1.Condition {
	return &policyv1.Condition{Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_OWNER_TEAM}
}

func status(allowed ...string) *policyv1.Condition {
	return &policyv1.Condition{
		Attribute:       policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_STATUS,
		AllowedStatuses: allowed,
	}
}

func classification() *policyv1.Condition {
	return &policyv1.Condition{Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_CLASSIFICATION}
}

func timeWindow(start, end uint32, tz string) *policyv1.Condition {
	return &policyv1.Condition{
		Attribute:  policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_TIME_WINDOW,
		TimeWindow: &policyv1.TimeWindow{StartMinute: start, EndMinute: end, Timezone: tz},
	}
}

func TestOwnerTeam(t *testing.T) {
	c := ownerTeam()

	allowed, err := EvaluateAll([]*policyv1.Condition{c}, Input{
		CallerTeamIDs:     []string{"team-a", "team-b"},
		RecordOwnerTeamID: "team-b",
	})
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = EvaluateAll([]*policyv1.Condition{c}, Input{
		CallerTeamIDs:     []string{"team-a"},
		RecordOwnerTeamID: "team-b",
	})
	require.NoError(t, err)
	require.False(t, allowed)

	// An unowned record denies rather than matching an empty caller team.
	allowed, err = EvaluateAll([]*policyv1.Condition{c}, Input{CallerTeamIDs: []string{""}})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestStatus(t *testing.T) {
	c := status("published", "archived")

	allowed, err := EvaluateAll([]*policyv1.Condition{c}, Input{RecordStatus: "published"})
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = EvaluateAll([]*policyv1.Condition{c}, Input{RecordStatus: "draft"})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestClassification(t *testing.T) {
	c := classification()

	allowed, err := EvaluateAll([]*policyv1.Condition{c}, Input{RecordClassification: 2, CallerClearance: 3})
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = EvaluateAll([]*policyv1.Condition{c}, Input{RecordClassification: 2, CallerClearance: 2})
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = EvaluateAll([]*policyv1.Condition{c}, Input{RecordClassification: 3, CallerClearance: 1})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestTimeWindow(t *testing.T) {
	c := timeWindow(9*60, 17*60, "UTC") // 09:00–17:00 UTC

	within := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	allowed, err := EvaluateAll([]*policyv1.Condition{c}, Input{Now: within})
	require.NoError(t, err)
	require.True(t, allowed)

	// End is exclusive.
	allowed, err = EvaluateAll([]*policyv1.Condition{c}, Input{Now: time.Date(2026, 8, 19, 17, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.False(t, allowed)

	// The window is evaluated in its own timezone: 12:00 UTC is 08:00 in New
	// York, before the 09:00 opening.
	ny := timeWindow(9*60, 17*60, "America/New_York")
	allowed, err = EvaluateAll([]*policyv1.Condition{ny}, Input{Now: within})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestEvaluateAllRequiresEveryCondition(t *testing.T) {
	in := Input{
		CallerTeamIDs:     []string{"team-a"},
		RecordOwnerTeamID: "team-a",
		RecordStatus:      "draft",
	}
	allowed, err := EvaluateAll([]*policyv1.Condition{ownerTeam(), status("published")}, in)
	require.NoError(t, err)
	require.False(t, allowed, "one failing condition denies the whole set")

	allowed, err = EvaluateAll(nil, in)
	require.NoError(t, err)
	require.True(t, allowed, "no conditions imposes no attribute constraint")
}

func TestEvaluateFailsClosedOnUnknownAttribute(t *testing.T) {
	unknown := &policyv1.Condition{Attribute: policyv1.ConditionAttribute(99)}
	allowed, err := EvaluateAll([]*policyv1.Condition{unknown}, Input{})
	require.Error(t, err)
	require.False(t, allowed)

	unspecified := &policyv1.Condition{Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_UNSPECIFIED}
	allowed, err = EvaluateAll([]*policyv1.Condition{unspecified}, Input{})
	require.Error(t, err)
	require.False(t, allowed)
}

func TestValidate(t *testing.T) {
	valid := []*policyv1.Condition{
		ownerTeam(),
		status("published"),
		classification(),
		timeWindow(0, minutesPerDay, "UTC"),
	}
	require.NoError(t, Validate(valid))

	cases := map[string]*policyv1.Condition{
		"unknown attribute":         {Attribute: policyv1.ConditionAttribute(42)},
		"unspecified attribute":     {Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_UNSPECIFIED},
		"status without values":     status(),
		"status with empty value":   status("open", ""),
		"owner-team with params":    {Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_OWNER_TEAM, AllowedStatuses: []string{"x"}},
		"window missing":            {Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_TIME_WINDOW},
		"window start after end":    timeWindow(600, 600, "UTC"),
		"window end beyond day":     timeWindow(0, minutesPerDay+1, "UTC"),
		"window bad timezone":       timeWindow(0, 60, "Mars/Phobos"),
		"window with statuses":      {Attribute: policyv1.ConditionAttribute_CONDITION_ATTRIBUTE_TIME_WINDOW, AllowedStatuses: []string{"x"}, TimeWindow: &policyv1.TimeWindow{StartMinute: 0, EndMinute: 60}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, Validate([]*policyv1.Condition{c}))
		})
	}
}
