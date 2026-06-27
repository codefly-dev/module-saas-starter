package business

import (
	"context"
	"hash/crc32"
)

// FeatureFlag represents a feature toggle.
type FeatureFlag struct {
	ID             string
	Name           string
	Description    string
	Enabled        bool
	RolloutPercent int
	TargetOrgIDs   []string
}

// FeatureChecker checks if a feature is available for an org.
// Combines feature flags (global toggle / rollout) with entitlements (plan-based).
type FeatureChecker interface {
	IsEnabled(ctx context.Context, orgID string, feature string) (bool, error)
}

// DefaultFeatureChecker combines flag checks with entitlement checks.
type DefaultFeatureChecker struct {
	store        Store
	entitlements EntitlementChecker
}

func NewDefaultFeatureChecker(store Store, entitlements EntitlementChecker) *DefaultFeatureChecker {
	return &DefaultFeatureChecker{store: store, entitlements: entitlements}
}

func (f *DefaultFeatureChecker) IsEnabled(ctx context.Context, orgID string, feature string) (bool, error) {
	// Check feature flag first (global toggle / rollout)
	flag, err := f.store.GetFeatureFlag(ctx, feature)
	if err != nil {
		return false, err
	}

	if flag != nil {
		if !flag.Enabled {
			return false, nil // globally disabled
		}

		// Check explicit org targeting
		for _, id := range flag.TargetOrgIDs {
			if id == orgID {
				goto entitlementCheck // explicitly targeted
			}
		}

		// Check rollout percentage (deterministic hash of orgID)
		if flag.RolloutPercent < 100 {
			h := crc32.ChecksumIEEE([]byte(orgID))
			if (h % 100) >= uint32(flag.RolloutPercent) {
				return false, nil // not in rollout
			}
		}
	}

entitlementCheck:
	// Check entitlement (plan-based access)
	if f.entitlements != nil {
		return f.entitlements.HasFeature(ctx, orgID, feature)
	}

	// No entitlement checker — feature flag alone determines access
	if flag != nil && flag.Enabled {
		return true, nil
	}
	return false, nil
}
