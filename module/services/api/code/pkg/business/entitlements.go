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
//
// Reads entitlement_overrides + subscriptions (both RLS-protected,
// Phase 2B) plus plan_entitlements (global). Bracket the per-tenant
// reads in WithOrgTx.
func (c *DefaultEntitlementChecker) GetLimit(ctx context.Context, orgID string, feature string) (int64, error) {
	var override *EntitlementOverride
	var planID string
	if err := c.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		o, err := c.store.GetEntitlementOverride(ctx, orgID, feature)
		if err != nil {
			return err
		}
		override = o
		p, err := c.store.GetOrgPlanID(ctx, orgID)
		planID = p
		return err
	}); err != nil {
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

	// plan_entitlements is global (skip-list), no wrap needed.
	limit, err := c.store.GetPlanEntitlement(ctx, planID, feature)
	if err != nil {
		return 0, err
	}
	return limit, nil
}

// CheckQuota checks if the org has remaining quota for a feature.
//
// All reads are tenant-scoped — wrap the whole switch in WithOrgTx
// so org_members, invitations, api_keys, usage_records (all RLS-
// protected) let the rows through.
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

	var used int64
	if err := c.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		switch feature {
		case "seats":
			members, err := c.store.ListOrgMembers(ctx, orgID)
			if err != nil {
				return err
			}
			pending, err := c.store.CountPendingInvitations(ctx, orgID)
			if err != nil {
				return err
			}
			used = int64(len(members)) + int64(pending)
		case "api_keys":
			keys, _, err := c.store.ListAPIKeys(ctx, orgID, 1000, "")
			if err != nil {
				return err
			}
			used = int64(len(keys))
		default:
			period := currentPeriod()
			u, err := c.store.GetUsageForPeriod(ctx, orgID, feature, period)
			used = u
			return err
		}
		return nil
	}); err != nil {
		return false, err
	}

	return used < limit, nil
}

// RecordUsage increments usage for a metered feature.
// usage_records is RLS-protected (Phase 2B).
func (c *DefaultEntitlementChecker) RecordUsage(ctx context.Context, orgID string, feature string, quantity int64) error {
	return c.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return c.store.RecordUsage(ctx, orgID, feature, quantity, currentPeriod())
	})
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
	// GetOrgPlanID reads subscriptions (RLS, Phase 2B); ListEntitlementOverrides
	// reads entitlement_overrides (RLS, Phase 2B). plans + plan_entitlements
	// are global (skip-list). Bracket all reads — including the
	// per-feature usage loop — in ONE WithOrgTx so SET LOCAL stays
	// valid across plan + overrides + every usage lookup. Hot admin
	// endpoint with N features → 1 transaction instead of N+2.
	out := &OrgEntitlementsView{}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		planID, err := s.store.GetOrgPlanID(ctx, orgID)
		if err != nil {
			return fmt.Errorf("resolve plan: %w", err)
		}
		plan, err := s.store.GetPlanByID(ctx, planID)
		if err != nil {
			return fmt.Errorf("load plan: %w", err)
		}
		planFeatures, err := s.store.ListPlanEntitlements(ctx, planID)
		if err != nil {
			return fmt.Errorf("list plan entitlements: %w", err)
		}
		overrides, err := s.store.ListEntitlementOverrides(ctx, orgID)
		if err != nil {
			return fmt.Errorf("list overrides: %w", err)
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
		out.PlanName = planNameOrFallback(plan)
		out.Entitlements = make([]OrgEntitlement, 0, len(planFeatures))

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
			used, err := resolveUsageInTx(ctx, s.store, orgID, fl.Feature, period)
			if err != nil {
				return fmt.Errorf("usage for %s: %w", fl.Feature, err)
			}
			out.Entitlements = append(out.Entitlements, OrgEntitlement{
				Feature:     fl.Feature,
				Limit:       limit,
				Used:        used,
				HasOverride: hasOverride,
			})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// resolveUsageInTx is the inner helper that assumes the caller is
// already inside a WithOrgTx (so the ctx carries the tx) — same
// branch logic as DefaultEntitlementChecker.CheckQuota but with the
// wrap moved out, letting GetOrgEntitlements bracket every per-
// feature read in one transaction. Don't call this from outside a
// tx — un-wrapped reads return zero rows under RLS.
func resolveUsageInTx(ctx context.Context, store Store, orgID, feature, period string) (int64, error) {
	switch feature {
	case "seats":
		members, err := store.ListOrgMembers(ctx, orgID)
		if err != nil {
			return 0, err
		}
		pending, err := store.CountPendingInvitations(ctx, orgID)
		if err != nil {
			return 0, err
		}
		return int64(len(members)) + int64(pending), nil
	case "api_keys":
		keys, _, err := store.ListAPIKeys(ctx, orgID, 1000, "")
		if err != nil {
			return 0, err
		}
		return int64(len(keys)), nil
	default:
		return store.GetUsageForPeriod(ctx, orgID, feature, period)
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
	// entitlement_overrides is RLS-protected (Phase 2B).
	if err := s.store.WithOrgTx(ctx, req.GetOrgId(), func(ctx context.Context) error {
		return s.store.CreateEntitlementOverride(ctx, override)
	}); err != nil {
		return "", fmt.Errorf("create override: %w", err)
	}
	s.emit(ctx, actorID, "user", "entitlement.override",
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
