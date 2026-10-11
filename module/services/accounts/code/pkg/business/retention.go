package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
)

type authenticationCeremonyRetentionStore interface {
	DeleteExpiredAuthenticationCeremonies(ctx context.Context, before time.Time) (webauthn, mfaLogin, clientCodes int64, err error)
}

type githubAppSetupRetentionStore interface {
	DeleteExpiredGitHubAppSetups(ctx context.Context, before time.Time) (int64, error)
}

// earliestStoredInstant is the earliest instant a PostgreSQL timestamp with
// time zone can hold, 24 November 4714 BC at 00:00 UTC. No record is older.
var earliestStoredInstant = time.Date(-4713, time.November, 24, 0, 0, 0, 0, time.UTC)

// retentionCutoff returns the instant before which a record has outlived a
// window of days, counted back from now by the calendar, and whether any record
// can be that old. A window that is not a positive number of days is an error:
// read literally, zero days reaches every record written before now, and fewer
// reach records written after it. Counting by the calendar keeps the cutoff
// before now however long the window is; a window reaching back past
// earliestStoredInstant has nothing older than its cutoff to remove.
func retentionCutoff(now time.Time, days int) (before time.Time, anyOlder bool, err error) {
	if days <= 0 {
		return time.Time{}, false, fmt.Errorf("a retention window is a positive number of days, not %d", days)
	}
	before = now.AddDate(0, 0, -days)
	return before, !before.Before(earliestStoredInstant), nil
}

// RunRetention loads all data retention policies and deletes records older
// than the configured retention period for each resource type. Returns a
// summary of deleted counts per resource type. A policy whose window is not a
// positive number of days removes nothing and is reported as a warning.
//
// Cross-tenant by nature: retention is a platform-wide cleanup that
// must touch every tenant's rows. audit_events + webhook_deliveries
// are RLS-protected — wrap each delete in WithControlPlane so the policy
// lets the cross-tenant scan through. sessions and notifications are
// in the skip-list (user-scoped) but we still bypass for symmetry. The audit
// partition drop runs in a control-plane transaction the store opens for it.
func (s *Service) RunRetention(ctx context.Context) (map[string]int64, error) {
	w := wool.Get(ctx).In("RunRetention")

	policies, err := s.store.GetRetentionPolicies(ctx)
	if err != nil {
		return nil, w.Wrapf(err, "cannot load retention policies")
	}

	now := time.Now()
	deleted := make(map[string]int64)
	for _, p := range policies {
		if p.ResourceType == "audit_events" {
			// audit_events is append-only and range-partitioned by created_at:
			// retention drops whole partitions older than the window (DDL that
			// sidesteps the immutability trigger) rather than issuing a row
			// DELETE the trigger would reject. Provision upcoming partitions on
			// every cycle, whatever the window, so writes never hit a missing
			// range.
			if ensureErr := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
				return s.store.EnsureAuditPartitions(ctx, 3)
			}); ensureErr != nil {
				w.Warn(fmt.Sprintf("audit partition provisioning failed: %v", ensureErr))
			}
		}

		before, anyOlder, windowErr := retentionCutoff(now, p.RetentionDays)
		if windowErr != nil {
			w.Warn(fmt.Sprintf("retention for %s removes nothing: %v", p.ResourceType, windowErr))
			continue
		}

		var count int64
		var bypassErr error
		switch {
		case !anyOlder:
			// Nothing is older than the cutoff.
		case p.ResourceType == "audit_events":
			// The drop decides which partitions are due once it holds its lock,
			// from the catalog as it stands then, which takes a transaction of
			// its own: the store opens one at READ COMMITTED, outside the one
			// that provisioned partitions above.
			count, bypassErr = s.store.DropAuditPartitionsBefore(ctx, before)
		default:
			bypassErr = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
				var bErr error
				switch p.ResourceType {
				case "sessions":
					count, bErr = s.store.DeleteOldSessions(ctx, before)
				case "webhook_deliveries":
					count, bErr = s.store.DeleteOldWebhookDeliveries(ctx, before)
				case "notifications":
					count, bErr = s.store.DeleteOldNotifications(ctx, before)
				default:
					w.Debug("unknown retention resource type", wool.Field("type", p.ResourceType))
				}
				return bErr
			})
		}

		if bypassErr != nil {
			w.Warn(fmt.Sprintf("retention cleanup for %s failed: %v", p.ResourceType, bypassErr))
			continue
		}
		deleted[p.ResourceType] = count
		if count > 0 {
			w.Debug(fmt.Sprintf("retention: deleted %d %s records older than %d days",
				count, p.ResourceType, p.RetentionDays))
		}
	}

	// Authentication hand-offs are operational security state, not customer
	// retention data. Remove expired rows after a short forensic window even
	// when an operator has not configured a generic retention policy.
	if ceremonyStore, ok := s.store.(authenticationCeremonyRetentionStore); ok {
		bypassErr := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			webauthn, mfaLogin, clientCodes, err := ceremonyStore.DeleteExpiredAuthenticationCeremonies(ctx, time.Now().Add(-24*time.Hour))
			deleted["webauthn_ceremonies"] = webauthn
			deleted["mfa_login_transactions"] = mfaLogin
			deleted["client_authorization_codes"] = clientCodes
			return err
		})
		if bypassErr != nil {
			w.Warn("authentication ceremony cleanup failed", wool.ErrField(bypassErr))
		}
	}

	// A GitHub App setup row is the same kind of state: a short-lived hand-off
	// that is dead once redeemed or lapsed, and that nothing else deletes. Swept
	// on the same forensic window so an abandoned connect attempt does not leave
	// a row behind permanently.
	if setupStore, ok := s.store.(githubAppSetupRetentionStore); ok {
		bypassErr := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			setups, err := setupStore.DeleteExpiredGitHubAppSetups(ctx, time.Now().Add(-24*time.Hour))
			deleted["github_app_setups"] = setups
			return err
		})
		if bypassErr != nil {
			w.Warn("github app setup cleanup failed", wool.ErrField(bypassErr))
		}
	}

	return deleted, nil
}

// PurgeExpiredWorkContextReplays reclaims single-use replay markers whose
// capability has expired. It is swept on its own short cadence rather than in
// RunRetention: a marker's TTL is the token's expiry (≤ 15 minutes), so folding
// it into the daily retention pass would let expired markers accumulate for up
// to a day. Cross-tenant, so it runs under the control-plane role.
func (s *Service) PurgeExpiredWorkContextReplays(ctx context.Context) (int64, error) {
	replayStore, ok := s.store.(WorkContextReplayStore)
	if !ok {
		return 0, nil
	}
	var count int64
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var purgeErr error
		count, purgeErr = replayStore.PurgeExpiredWorkContextReplays(ctx, time.Now())
		return purgeErr
	})
	return count, err
}
