package business

import (
	"context"
	"fmt"
	"time"
)

// EntitlementChecker checks feature access and quota limits for an org.
type EntitlementChecker interface {
	HasFeature(ctx context.Context, orgID string, feature string) (bool, error)
	GetLimit(ctx context.Context, orgID string, feature string) (int64, error)
	CheckQuota(ctx context.Context, orgID string, feature string) (bool, error)
	RecordUsage(ctx context.Context, orgID string, feature string, quantity int64) error
}

// Plan represents a subscription plan.
type Plan struct {
	ID          string
	Name        string
	DisplayName string
	IsDefault   bool
	SortOrder   int
}

// Subscription links an org to a plan.
type Subscription struct {
	ID                   string
	OrgID                string
	PlanID               string
	Status               string
	StripeSubscriptionID string
	CurrentPeriodStart   *time.Time
	CurrentPeriodEnd     *time.Time
}

// EntitlementOverride is a per-org feature limit override.
type EntitlementOverride struct {
	ID         string
	OrgID      string
	Feature    string
	LimitValue *int64
	Reason     string
	CreatedBy  string
	ExpiresAt  *time.Time
}

// DefaultEntitlementChecker resolves entitlements from plan + overrides.
type DefaultEntitlementChecker struct {
	store Store
}

func NewDefaultEntitlementChecker(store Store) *DefaultEntitlementChecker {
	return &DefaultEntitlementChecker{store: store}
}

// HasFeature checks if an org has access to a boolean feature.
// A feature is enabled if its limit is > 0 (or NULL = unlimited).
func (c *DefaultEntitlementChecker) HasFeature(ctx context.Context, orgID string, feature string) (bool, error) {
	limit, err := c.GetLimit(ctx, orgID, feature)
	if err != nil {
		return false, err
	}
	return limit != 0, nil // 0 = disabled, anything else (including -1 unlimited) = enabled
}

// GetLimit returns the effective limit for a feature.
// Returns -1 for unlimited, 0 for disabled/not-in-plan.
func (c *DefaultEntitlementChecker) GetLimit(ctx context.Context, orgID string, feature string) (int64, error) {
	// Check override first
	override, err := c.store.GetEntitlementOverride(ctx, orgID, feature)
	if err != nil {
		return 0, err
	}
	if override != nil {
		if override.ExpiresAt != nil && override.ExpiresAt.Before(time.Now()) {
			// Override expired, fall through to plan
		} else if override.LimitValue == nil {
			return -1, nil // unlimited
		} else {
			return *override.LimitValue, nil
		}
	}

	// Get org's plan
	planID, err := c.store.GetOrgPlanID(ctx, orgID)
	if err != nil {
		return 0, err
	}

	// Get plan entitlement
	limit, err := c.store.GetPlanEntitlement(ctx, planID, feature)
	if err != nil {
		return 0, err
	}
	return limit, nil
}

// CheckQuota checks if the org has remaining quota for a feature.
func (c *DefaultEntitlementChecker) CheckQuota(ctx context.Context, orgID string, feature string) (bool, error) {
	limit, err := c.GetLimit(ctx, orgID, feature)
	if err != nil {
		return false, err
	}
	if limit == -1 {
		return true, nil // unlimited
	}
	if limit == 0 {
		return false, nil // disabled
	}

	// Get current usage
	var used int64
	switch feature {
	case "seats":
		members, err := c.store.ListOrgMembers(ctx, orgID)
		if err != nil {
			return false, err
		}
		pending, err := c.store.CountPendingInvitations(ctx, orgID)
		if err != nil {
			return false, err
		}
		used = int64(len(members)) + int64(pending)
	case "api_keys":
		keys, _, err := c.store.ListAPIKeys(ctx, orgID, 1000, "")
		if err != nil {
			return false, err
		}
		used = int64(len(keys))
	default:
		// Metered features use usage_records
		period := currentPeriod()
		used, err = c.store.GetUsageForPeriod(ctx, orgID, feature, period)
		if err != nil {
			return false, err
		}
	}

	return used < limit, nil
}

// RecordUsage increments usage for a metered feature.
func (c *DefaultEntitlementChecker) RecordUsage(ctx context.Context, orgID string, feature string, quantity int64) error {
	return c.store.RecordUsage(ctx, orgID, feature, quantity, currentPeriod())
}

func currentPeriod() string {
	return fmt.Sprintf("%d-%02d", time.Now().Year(), time.Now().Month())
}
