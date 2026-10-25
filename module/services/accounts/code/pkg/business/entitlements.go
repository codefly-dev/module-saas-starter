package business

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
)

var ErrEntitlementQuotaExceeded = errors.New("entitlement quota exceeded")

// CardinalityQuota is a transactionally consistent view of a live-resource
// allowance. Event meters do not use this type; they go through ConsumeUsage.
type CardinalityQuota struct {
	Feature string
	Used    int64
	Limit   int64
}

func (q CardinalityQuota) Available() bool {
	return q.Limit == -1 || q.Used < q.Limit
}

// EntitlementQuotaError is safe to inspect with errors.Is while retaining the
// feature and observed counts for logs and protocol mapping.
type EntitlementQuotaError struct {
	Feature string
	Used    int64
	Limit   int64
}

func (e *EntitlementQuotaError) Error() string {
	return fmt.Sprintf("%s quota exhausted: used %d of %d", e.Feature, e.Used, e.Limit)
}

func (e *EntitlementQuotaError) Unwrap() error { return ErrEntitlementQuotaExceeded }

func (q CardinalityQuota) RequireAvailable() error {
	if q.Available() {
		return nil
	}
	return &EntitlementQuotaError{Feature: q.Feature, Used: q.Used, Limit: q.Limit}
}

// EntitlementChecker resolves feature access and transaction-scoped
// cardinality quotas for an organization. It is the one place an effective
// limit is decided: the cardinality quotas, the metered usage ledger, the
// usage read-outs and the entitlement view all ask it.
type EntitlementChecker interface {
	HasFeature(ctx context.Context, orgID string, feature string) (bool, error)
	GetLimit(ctx context.Context, orgID string, feature string) (int64, error)
	CheckCardinalityQuotaInTx(ctx context.Context, orgID string, feature string) (CardinalityQuota, error)
	// EffectiveLimitInTx resolves one feature's limit inside the caller's
	// tenant transaction: -1 unlimited, 0 disabled, >0 a cap.
	EffectiveLimitInTx(ctx context.Context, orgID string, feature string, now time.Time) (int64, error)
}

// PlanPolicy says whether this deployment sells plans. A plan's limits are
// what a customer bought; a deployment that sells none has nothing to hold an
// organization to, so it enforces no plan limit at all — not the default plan's
// seats, API keys or meters, and not its disabled features.
type PlanPolicy int

const (
	// PlansSold enforces each organization's plan and its overrides.
	PlansSold PlanPolicy = iota + 1
	// NoPlansSold resolves every feature as unlimited.
	NoPlansSold
)

// PlanPolicyForBillingProvider maps the billing configuration's provider to the
// plan policy. A deployment sells plans exactly when it takes payment for them:
// only a configured billing provider can move an organization off the default
// plan, so without one the plan rows describe nothing anyone bought.
func PlanPolicyForBillingProvider(provider string) PlanPolicy {
	if provider == "stripe" {
		return PlansSold
	}
	return NoPlansSold
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
	CheckoutEnabled bool
	TrialDays       int
	TaxBehavior     string
}

type PublicPlan struct {
	Key             string
	Name            string
	Description     string
	Currency        string
	AmountMinor     int64
	Interval        string
	CheckoutEnabled bool
	ContactSales    bool
	TrialDays       int
	TaxBehavior     string
	Fixture         bool
	Entitlements    []PlanFeatureLimit
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

// DefaultEntitlementChecker resolves entitlements from plan + overrides when
// the deployment sells plans, and as unlimited when it does not.
type DefaultEntitlementChecker struct {
	store Store
	plans PlanPolicy
}

func NewDefaultEntitlementChecker(store Store, plans PlanPolicy) *DefaultEntitlementChecker {
	return &DefaultEntitlementChecker{store: store, plans: plans}
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
	if c.plans != PlansSold {
		return -1, nil
	}
	var limit int64
	if err := c.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		limit, err = c.EffectiveLimitInTx(ctx, orgID, feature, time.Now().UTC())
		return err
	}); err != nil {
		return 0, err
	}
	return limit, nil
}

// EffectiveLimitInTx is the override-then-plan resolution when the deployment
// sells plans, and -1 (unlimited) when it does not. The caller holds the
// tenant transaction.
func (c *DefaultEntitlementChecker) EffectiveLimitInTx(ctx context.Context, orgID string, feature string, now time.Time) (int64, error) {
	if c.plans != PlansSold {
		return -1, nil
	}
	return resolveEffectiveLimitInTx(ctx, c.store, orgID, feature, now)
}

func resolveEffectiveLimitInTx(ctx context.Context, store Store, orgID, feature string, now time.Time) (int64, error) {
	override, err := store.GetEntitlementOverride(ctx, orgID, feature)
	if err != nil {
		return 0, err
	}
	if override != nil {
		if override.ExpiresAt != nil && override.ExpiresAt.Before(now) {
			// Override expired, fall through to plan
		} else if override.LimitValue == nil {
			return -1, nil // unlimited
		} else {
			return *override.LimitValue, nil
		}
	}

	planID, err := store.GetOrgPlanID(ctx, orgID)
	if err != nil {
		return 0, err
	}
	limit, err := store.GetPlanEntitlement(ctx, planID, feature)
	if err != nil {
		return 0, err
	}
	return limit, nil
}

// CheckCardinalityQuotaInTx serializes one tenant/feature admission decision,
// then resolves its limit and authoritative live-resource count. The caller
// must keep the returned decision and the resource write in the same
// transaction. This method intentionally rejects event meters: those use the
// immutable ConsumeUsage ledger and aggregate row lock instead.
func (c *DefaultEntitlementChecker) CheckCardinalityQuotaInTx(ctx context.Context, orgID string, feature string) (CardinalityQuota, error) {
	if feature != EntitlementSeats && feature != EntitlementAPIKeys {
		return CardinalityQuota{}, fmt.Errorf("%s is not a cardinality entitlement", feature)
	}
	if err := c.store.LockEntitlementQuota(ctx, orgID, feature); err != nil {
		return CardinalityQuota{}, err
	}
	limit, err := c.EffectiveLimitInTx(ctx, orgID, feature, time.Now().UTC())
	if err != nil {
		return CardinalityQuota{}, err
	}
	used, err := resolveUsageInTx(ctx, c.store, orgID, feature, time.Time{})
	if err != nil {
		return CardinalityQuota{}, err
	}
	return CardinalityQuota{Feature: feature, Used: used, Limit: limit}, nil
}

var errEntitlementCheckerMissing = errors.New("entitlement checker is not configured")

func (s *Service) cardinalityQuotaInTx(ctx context.Context, orgID string, feature string) (CardinalityQuota, error) {
	if s.entitlements == nil {
		return CardinalityQuota{}, errEntitlementCheckerMissing
	}
	return s.entitlements.CheckCardinalityQuotaInTx(ctx, orgID, feature)
}

// admitCardinality refuses a live resource the quota has no room for, and logs
// the refusal: a quota is a business rule a person hits, and an operator asked
// "why was this refused" must find the answer in the log, not only in the
// caller's response. Ids and counts only.
func admitCardinality(ctx context.Context, orgID string, quota CardinalityQuota) error {
	err := quota.RequireAvailable()
	if err != nil {
		logQuotaRefusal(ctx, orgID, quota.Feature, quota.Used, quota.Limit)
	}
	return err
}

func logQuotaRefusal(ctx context.Context, orgID, feature string, used, limit int64) {
	wool.Get(ctx).Warn("entitlement quota refused",
		wool.Field("org_id", orgID),
		wool.Field("feature", feature),
		wool.Field("used", used),
		wool.Field("limit", limit))
}

func (s *Service) effectiveLimitInTx(ctx context.Context, orgID, feature string, now time.Time) (int64, error) {
	if s.entitlements == nil {
		return 0, errEntitlementCheckerMissing
	}
	return s.entitlements.EffectiveLimitInTx(ctx, orgID, feature, now)
}

// GetOrgEntitlements returns the resolved view (plan name + per-
// feature limit/used/has_override) for the org. Powers both the
// platform-admin Entitlements page and the org-side Usage dashboard.
//
// Resolution order per feature:
//  1. plan_entitlements row gives the base limit
//  2. entitlement_overrides row replaces it (unless expired)
//  3. usage is read from usage_totals for metered features, or
//     computed from cardinality (seats = members + live pending invites,
//     api_keys = active non-expired/non-revoked rows) for the two non-metered ones
//     that the EntitlementChecker already knows about.
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

		periodStart, _ := monthlyUsagePeriod(now)
		out.PlanName = planNameOrFallback(plan)
		out.Entitlements = make([]OrgEntitlement, 0, len(planFeatures))

		for _, fl := range planFeatures {
			// The same resolution every admission uses, so the view never
			// shows a limit the deployment does not enforce.
			limit, err := s.effectiveLimitInTx(ctx, orgID, fl.Feature, now)
			if err != nil {
				return fmt.Errorf("limit for %s: %w", fl.Feature, err)
			}
			_, hasOverride := overrideMap[fl.Feature]
			used, err := resolveUsageInTx(ctx, s.store, orgID, fl.Feature, periodStart)
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
// branch logic as DefaultEntitlementChecker.CheckCardinalityQuotaInTx but with the
// wrap moved out, letting GetOrgEntitlements bracket every per-
// feature read in one transaction. Don't call this from outside a
// tx — un-wrapped reads return zero rows under RLS.
func resolveUsageInTx(ctx context.Context, store Store, orgID, feature string, periodStart time.Time) (int64, error) {
	switch feature {
	case EntitlementSeats:
		members, err := store.ListOrgMembers(ctx, orgID)
		if err != nil {
			return 0, err
		}
		pending, err := store.CountPendingInvitations(ctx, orgID)
		if err != nil {
			return 0, err
		}
		return int64(len(members)) + int64(pending), nil
	case EntitlementAPIKeys:
		return store.CountActiveAPIKeys(ctx, orgID)
	default:
		return store.GetUsageTotal(ctx, orgID, feature, periodStart)
	}
}

// OverrideEntitlement creates or refreshes a per-org override for a
// feature limit. Used by the platform-admin dashboard to grant a
// customer extra capacity (or revoke it) without changing their plan.
//
// limitValue semantics:
//
//	-1  → unlimited (stored as NULL via LimitValue=nil)
//	 0  → disabled
//	>0  → the new metered cap
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
		if err := s.store.CreateEntitlementOverride(ctx, override); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventEntitlementOverride,
			"entitlement_override", override.ID, req.GetOrgId())
	}); err != nil {
		return "", fmt.Errorf("create override: %w", err)
	}
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
