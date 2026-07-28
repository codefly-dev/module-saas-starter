package analytics

import (
	"errors"
	"fmt"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
)

func CheckCompatible(previous, current *Registry) error {
	if previous == nil || current == nil {
		return errors.New("analytics: previous and current registries are required")
	}
	if current.contractVersion < previous.contractVersion {
		return errors.New("analytics: registry contract version cannot decrease")
	}
	for _, before := range previous.Definitions() {
		after, ok := current.Definition(before.Name)
		if !ok {
			return fmt.Errorf("analytics: registered event %q was removed", before.Name)
		}
		if after.SchemaVersion < before.SchemaVersion {
			return fmt.Errorf("analytics: event %q schema version decreased", before.Name)
		}
		if after.SchemaVersion > before.SchemaVersion {
			continue
		}
		if after.Purpose != before.Purpose ||
			after.PIIClassification != before.PIIClassification ||
			!sameSources(after.Sources, before.Sources) {
			return fmt.Errorf(
				"analytics: event %q changed purpose, privacy, or source without a schema version",
				before.Name,
			)
		}
		for property, propertyType := range before.PropertyTypes {
			currentType, exists := after.PropertyTypes[property]
			if !exists {
				return fmt.Errorf(
					"analytics: event %q removed property %q without a schema version",
					before.Name,
					property,
				)
			}
			if currentType != propertyType {
				return fmt.Errorf(
					"analytics: event %q changed property %q type without a schema version",
					before.Name,
					property,
				)
			}
		}
	}
	return nil
}

func sameSources(left, right []analyticsv1.EventSource) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[analyticsv1.EventSource]int, len(left))
	for _, source := range left {
		counts[source]++
	}
	for _, source := range right {
		counts[source]--
		if counts[source] < 0 {
			return false
		}
	}
	return true
}
