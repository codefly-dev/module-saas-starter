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

// PlanFull adds the Stripe mapping columns. Returned by GetPlanByName
// for the billing.StartCheckout flow.
type PlanFull struct {
	Plan
	StripeProductID string
	StripePriceID   string
	Currency        string // ISO 4217, e.g. "usd", "eur"
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

// PlanFeatureLimit — one row from plan_entitlements. Limit is -1 for
// unlimited (NULL in DB), 0 for disabled, positive for a metered cap.
type PlanFeatureLimit struct {
	Feature string
	Limit   int64
}

// OrgEntitlement is the resolved view a single feature for an org —
// limit (after override), current usage, and whether an override is
// in play. Returned by Service.GetOrgEntitlements; consumed by the
// admin Entitlements page and the billing/usage dashboard.
type OrgEntitlement struct {
	Feature     string
	Limit       int64 // -1 unlimited, 0 disabled, >0 metered cap
	Used        int64
	HasOverride bool
}

// OrgEntitlementsView packages plan name + per-feature entitlements
// for the FE in one shot. Saves a round-trip per feature.
type OrgEntitlementsView struct {
	PlanName     string
	Entitlements []OrgEntitlement
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

// GetOrgEntitlements returns the resolved view (plan name + per-
// feature limit/used/has_override) for the org. Powers both the
// platform-admin Entitlements page and the org-side Usage dashboard.
//
// Resolution order per feature:
//   1. plan_entitlements row gives the base limit
//   2. entitlement_overrides row replaces it (unless expired)
//   3. usage is read from usage_records for metered features, or
//      computed from cardinality (seats = members + pending invites,
//      api_keys = ListAPIKeys count) for the two non-metered ones
//      that the EntitlementChecker already knows about.
//
// Authz expectation: caller is org-admin OR platform-admin, gated by
// the adapter — this method trusts its inputs.
func (s *Service) GetOrgEntitlements(ctx context.Context, orgID string) (*OrgEntitlementsView, error) {
	planID, err := s.store.GetOrgPlanID(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("resolve plan: %w", err)
	}
	plan, err := s.store.GetPlanByID(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("load plan: %w", err)
	}

	planFeatures, err := s.store.ListPlanEntitlements(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list plan entitlements: %w", err)
	}

	overrides, err := s.store.ListEntitlementOverrides(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list overrides: %w", err)
	}
	overrideMap := make(map[string]*EntitlementOverride, len(overrides))
	now := time.Now()
	for _, o := range overrides {
		// Expired overrides fall through to the plan limit.
		if o.ExpiresAt != nil && o.ExpiresAt.Before(now) {
			continue
		}
		overrideMap[o.Feature] = o
	}

	period := currentPeriod()
	out := &OrgEntitlementsView{
		PlanName:     planNameOrFallback(plan),
		Entitlements: make([]OrgEntitlement, 0, len(planFeatures)),
	}

	for _, fl := range planFeatures {
		limit := fl.Limit
		hasOverride := false
		if o, ok := overrideMap[fl.Feature]; ok {
			hasOverride = true
			if o.LimitValue == nil {
				limit = -1 // override → unlimited
			} else {
				limit = *o.LimitValue
			}
		}
		used, err := s.resolveUsage(ctx, orgID, fl.Feature, period)
		if err != nil {
			return nil, fmt.Errorf("usage for %s: %w", fl.Feature, err)
		}
		out.Entitlements = append(out.Entitlements, OrgEntitlement{
			Feature:     fl.Feature,
			Limit:       limit,
			Used:        used,
			HasOverride: hasOverride,
		})
	}

	return out, nil
}

// resolveUsage mirrors DefaultEntitlementChecker.CheckQuota's branch:
// non-metered features (seats, api_keys) are computed from
// cardinality, metered features come from usage_records.
func (s *Service) resolveUsage(ctx context.Context, orgID, feature, period string) (int64, error) {
	switch feature {
	case "seats":
		members, err := s.store.ListOrgMembers(ctx, orgID)
		if err != nil {
			return 0, err
		}
		pending, err := s.store.CountPendingInvitations(ctx, orgID)
		if err != nil {
			return 0, err
		}
		return int64(len(members)) + int64(pending), nil
	case "api_keys":
		keys, _, err := s.store.ListAPIKeys(ctx, orgID, 1000, "")
		if err != nil {
			return 0, err
		}
		return int64(len(keys)), nil
	default:
		return s.store.GetUsageForPeriod(ctx, orgID, feature, period)
	}
}

// OverrideEntitlement creates or refreshes a per-org override for a
// feature limit. Used by the platform-admin dashboard to grant a
// customer extra capacity (or revoke it) without changing their plan.
//
// limitValue semantics:
//   -1  → unlimited (stored as NULL via LimitValue=nil)
//    0  → disabled
//   >0  → the new metered cap
//
// Authz expectation: caller is platform-admin, gated by the adapter.
// Emits an audit event for the operator trail.
func (s *Service) OverrideEntitlement(ctx context.Context, actorID string, req interface {
	GetOrgId() string
	GetFeature() string
	GetLimitValue() int64
	GetReason() string
}) (string, error) {
	override := &EntitlementOverride{
		ID:        NewIDString(),
		OrgID:     req.GetOrgId(),
		Feature:   req.GetFeature(),
		Reason:    req.GetReason(),
		CreatedBy: actorID,
	}
	limit := req.GetLimitValue()
	if limit == -1 {
		// nil = unlimited (matches NULL in DB column).
		override.LimitValue = nil
	} else {
		v := limit
		override.LimitValue = &v
	}
	if err := s.store.CreateEntitlementOverride(ctx, override); err != nil {
		return "", fmt.Errorf("create override: %w", err)
	}
	s.emit(ctx, req.GetOrgId(), "user", "entitlement.override",
		"entitlement_override", override.ID, req.GetOrgId())
	return override.ID, nil
}

func planNameOrFallback(p *Plan) string {
	if p == nil {
		return "Free"
	}
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Name
}
