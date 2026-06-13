package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
)

// RunRetention loads all data retention policies and deletes records older
// than the configured retention period for each resource type. Returns a
// summary of deleted counts per resource type.
//
// Cross-tenant by nature: retention is a platform-wide cleanup that
// must touch every tenant's rows. audit_events + webhook_deliveries
// are RLS-protected — wrap each delete in WithBypass so the policy
// lets the cross-tenant scan through. sessions and notifications are
// in the skip-list (user-scoped) but we still bypass for symmetry.
func (s *Service) RunRetention(ctx context.Context) (map[string]int64, error) {
	w := wool.Get(ctx).In("RunRetention")

	policies, err := s.store.GetRetentionPolicies(ctx)
	if err != nil {
		return nil, w.Wrapf(err, "cannot load retention policies")
	}

	deleted := make(map[string]int64)
	for _, p := range policies {
		before := time.Now().Add(-time.Duration(p.RetentionDays) * 24 * time.Hour)

		var count int64
		bypassErr := s.store.WithBypass(ctx, func(ctx context.Context) error {
			var bErr error
			switch p.ResourceType {
			case "audit_events":
				count, bErr = s.store.DeleteOldAuditEvents(ctx, before)
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

	return deleted, nil
}
