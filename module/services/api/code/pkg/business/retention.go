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
		switch p.ResourceType {
		case "audit_events":
			count, err = s.store.DeleteOldAuditEvents(ctx, before)
		case "sessions":
			count, err = s.store.DeleteOldSessions(ctx, before)
		case "webhook_deliveries":
			count, err = s.store.DeleteOldWebhookDeliveries(ctx, before)
		case "notifications":
			count, err = s.store.DeleteOldNotifications(ctx, before)
		default:
			w.Debug("unknown retention resource type", wool.Field("type", p.ResourceType))
			continue
		}

		if err != nil {
			w.Warn(fmt.Sprintf("retention cleanup for %s failed: %v", p.ResourceType, err))
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
