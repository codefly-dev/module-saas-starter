package metrics

import (
	"errors"
	"sort"
	"time"
)

type ActivationDefinition struct {
	Version       uint32
	EffectiveFrom time.Time
	EventName     string
}

type ActivationEvent struct {
	EventID        string
	EventName      string
	OrganizationID string
	UserID         string
	OccurredAt     time.Time
}

type WeeklyActivation struct {
	WeekStart     time.Time
	Version       uint32
	Organizations int
	Users         int
}

func WeeklyActivated(
	events []ActivationEvent,
	definitions []ActivationDefinition,
	location *time.Location,
) ([]WeeklyActivation, error) {
	if location == nil {
		return nil, errors.New("metrics: activation timezone is required")
	}
	if len(definitions) == 0 {
		return nil, errors.New("metrics: at least one activation definition is required")
	}
	definitions = append([]ActivationDefinition(nil), definitions...)
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].EffectiveFrom.Before(definitions[j].EffectiveFrom)
	})
	versions := map[uint32]struct{}{}
	for index, definition := range definitions {
		if definition.Version == 0 || definition.EventName == "" ||
			definition.EffectiveFrom.IsZero() {
			return nil, errors.New("metrics: activation definition is incomplete")
		}
		if _, exists := versions[definition.Version]; exists {
			return nil, errors.New("metrics: activation definition versions must be unique")
		}
		versions[definition.Version] = struct{}{}
		if index > 0 && !definition.EffectiveFrom.After(definitions[index-1].EffectiveFrom) {
			return nil, errors.New("metrics: activation definition dates must be unique")
		}
	}

	type key struct {
		week    time.Time
		version uint32
	}
	type members struct {
		organizations map[string]struct{}
		users         map[string]struct{}
	}
	weeks := map[key]members{}
	seen := map[string]ActivationEvent{}
	for _, event := range events {
		if event.EventID == "" || event.OccurredAt.IsZero() {
			return nil, errors.New("metrics: activation event identity and time are required")
		}
		if existing, duplicate := seen[event.EventID]; duplicate {
			if existing != event {
				return nil, errors.New("metrics: activation event identity conflict")
			}
			continue
		}
		seen[event.EventID] = event
		definition, ok := activationDefinitionAt(definitions, event.OccurredAt)
		if !ok || event.EventName != definition.EventName {
			continue
		}
		if event.OrganizationID == "" && event.UserID == "" {
			return nil, errors.New("metrics: activation event requires an organization or user")
		}
		week := weekStart(event.OccurredAt, location)
		bucketKey := key{week: week, version: definition.Version}
		bucket := weeks[bucketKey]
		if bucket.organizations == nil {
			bucket.organizations = map[string]struct{}{}
			bucket.users = map[string]struct{}{}
		}
		if event.OrganizationID != "" {
			bucket.organizations[event.OrganizationID] = struct{}{}
		}
		if event.UserID != "" {
			bucket.users[event.UserID] = struct{}{}
		}
		weeks[bucketKey] = bucket
	}

	result := make([]WeeklyActivation, 0, len(weeks))
	for bucketKey, bucket := range weeks {
		result = append(result, WeeklyActivation{
			WeekStart:     bucketKey.week,
			Version:       bucketKey.version,
			Organizations: len(bucket.organizations),
			Users:         len(bucket.users),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WeekStart.Equal(result[j].WeekStart) {
			return result[i].Version < result[j].Version
		}
		return result[i].WeekStart.Before(result[j].WeekStart)
	})
	return result, nil
}

func activationDefinitionAt(
	definitions []ActivationDefinition,
	occurredAt time.Time,
) (ActivationDefinition, bool) {
	index := sort.Search(len(definitions), func(index int) bool {
		return definitions[index].EffectiveFrom.After(occurredAt)
	})
	if index == 0 {
		return ActivationDefinition{}, false
	}
	return definitions[index-1], true
}

func weekStart(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	return time.Date(
		local.Year(),
		local.Month(),
		local.Day()-daysSinceMonday,
		0,
		0,
		0,
		0,
		location,
	)
}
