package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWeeklyActivatedDeduplicatesAndVersionsDefinitions(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)
	versionTwo := time.Date(2026, time.July, 27, 22, 0, 0, 0, time.UTC)
	events := []ActivationEvent{
		{
			EventID:        "first",
			EventName:      "core_action_completed",
			OrganizationID: "alpha",
			UserID:         "user-1",
			OccurredAt:     time.Date(2026, time.July, 26, 23, 30, 0, 0, time.UTC),
		},
		{
			EventID:        "first",
			EventName:      "core_action_completed",
			OrganizationID: "alpha",
			UserID:         "user-1",
			OccurredAt:     time.Date(2026, time.July, 26, 23, 30, 0, 0, time.UTC),
		},
		{
			EventID:        "second",
			EventName:      "document_published",
			OrganizationID: "bravo",
			UserID:         "user-2",
			OccurredAt:     versionTwo.Add(time.Hour),
		},
		{
			EventID:        "third",
			EventName:      "core_action_completed",
			OrganizationID: "ignored",
			OccurredAt:     versionTwo.Add(time.Hour),
		},
	}

	weekly, err := WeeklyActivated(events, []ActivationDefinition{
		{
			Version:       1,
			EffectiveFrom: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			EventName:     "core_action_completed",
		},
		{Version: 2, EffectiveFrom: versionTwo, EventName: "document_published"},
	}, paris)
	require.NoError(t, err)
	require.Equal(t, []WeeklyActivation{
		{
			WeekStart:     time.Date(2026, time.July, 27, 0, 0, 0, 0, paris),
			Version:       1,
			Organizations: 1,
			Users:         1,
		},
		{
			WeekStart:     time.Date(2026, time.July, 27, 0, 0, 0, 0, paris),
			Version:       2,
			Organizations: 1,
			Users:         1,
		},
	}, weekly)
}

func TestWeeklyActivatedRequiresVersionedDefinitions(t *testing.T) {
	_, err := WeeklyActivated(nil, []ActivationDefinition{
		{
			Version:       1,
			EffectiveFrom: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			EventName:     "core_action_completed",
		},
		{
			Version:       1,
			EffectiveFrom: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC),
			EventName:     "document_published",
		},
	}, time.UTC)
	require.ErrorContains(t, err, "versions")
}

func TestWeeklyActivatedHandlesEmptyInputAndRejectsConflictingRetries(t *testing.T) {
	definitions := []ActivationDefinition{{
		Version:       1,
		EffectiveFrom: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		EventName:     "core_action_completed",
	}}
	weekly, err := WeeklyActivated(nil, definitions, time.UTC)
	require.NoError(t, err)
	require.Empty(t, weekly)

	occurredAt := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	_, err = WeeklyActivated([]ActivationEvent{
		{
			EventID: "same", EventName: "core_action_completed",
			OrganizationID: "alpha", OccurredAt: occurredAt,
		},
		{
			EventID: "same", EventName: "core_action_completed",
			OrganizationID: "bravo", OccurredAt: occurredAt,
		},
	}, definitions, time.UTC)
	require.ErrorContains(t, err, "identity conflict")
}
