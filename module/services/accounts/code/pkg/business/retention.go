package business

import (
	"context"
	"fmt"
	"strings"
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
// window of days, counted back from now, and whether any record can be that
// old. A window that is not a positive number of days is an error: read
// literally, zero days reaches every record written before now, and fewer
// reach records written after it. A day is 24 hours wherever the process runs:
// the window is counted back in UTC, where every day has 24, so the cutoff is
// exactly days*24h before now, and stays before now however long the window
// is, where a time.Duration overflows past 106751 days. A window reaching back
// past earliestStoredInstant has nothing older than its cutoff to remove.
func retentionCutoff(now time.Time, days int) (before time.Time, anyOlder bool, err error) {
	if days <= 0 {
		return time.Time{}, false, fmt.Errorf("a retention window is a positive number of days, not %d", days)
	}
	before = now.UTC().AddDate(0, 0, -days)
	return before, !before.Before(earliestStoredInstant), nil
}

// RetentionStep names the part of a retention run that failed.
type RetentionStep string

const (
	// RetentionStepLoad is reading the policies, or the database clock every
	// cutoff is counted from.
	RetentionStepLoad RetentionStep = "load"
	// RetentionStepProvision is provisioning audit partitions.
	RetentionStepProvision RetentionStep = "provision"
	// RetentionStepPolicy is a policy no run can apply: its window is not a
	// positive number of days.
	RetentionStepPolicy RetentionStep = "policy"
	// RetentionStepRemove is removing a resource's expired records.
	RetentionStepRemove RetentionStep = "remove"
)

// RetentionFailure is one failed part of a retention run.
type RetentionFailure struct {
	Resource string
	Step     RetentionStep
	Err      error
}

// RetentionError is a retention run that did not do all it should. A run goes
// on past a failing part, so the error lists every failure, and the counts the
// run returns beside it are those of the parts that succeeded.
type RetentionError struct {
	Failures []RetentionFailure
}

func (e *RetentionError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = fmt.Sprintf("%s %s: %v", f.Resource, f.Step, f.Err)
	}
	return "retention failed: " + strings.Join(parts, "; ")
}

func (e *RetentionError) Unwrap() []error {
	errs := make([]error, len(e.Failures))
	for i, f := range e.Failures {
		errs[i] = f.Err
	}
	return errs
}

// RunRetention provisions audit partitions, then deletes, for each resource
// with a retention policy, the records older than its window, counted back from
// the database's clock. It returns the number of records removed per resource.
// A failing part does not stop the others, and every failure comes back in one
// *RetentionError beside the counts of the parts that succeeded, so the caller
// reports a run that did not do all it should rather than a log line.
//
// Cross-tenant by nature: retention is a platform-wide cleanup that
// must touch every tenant's rows. audit_events + webhook_deliveries
// are RLS-protected — wrap each delete in WithControlPlane so the policy
// lets the cross-tenant scan through. sessions and notifications are
// in the skip-list (user-scoped) but we still bypass for symmetry. The audit
// partition drop runs in a control-plane transaction the store opens for it.
func (s *Service) RunRetention(ctx context.Context) (map[string]int64, error) {
	w := wool.Get(ctx).In("RunRetention")

	deleted := make(map[string]int64)
	var failures []RetentionFailure
	fail := func(resource string, step RetentionStep, err error) {
		failures = append(failures, RetentionFailure{Resource: resource, Step: step, Err: err})
	}
	outcome := func() (map[string]int64, error) {
		if len(failures) > 0 {
			return deleted, &RetentionError{Failures: failures}
		}
		return deleted, nil
	}

	// Audit writes need the months ahead to have partitions whatever the
	// policies say, so they are provisioned on every run, policy or none.
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.EnsureAuditPartitions(ctx, 3)
	}); err != nil {
		fail("audit_events", RetentionStepProvision, err)
	}

	// Every cutoff is counted from the database's clock. The windows are the
	// database's records, and a process clock running ahead would otherwise
	// remove records still inside them.
	now, err := s.store.DatabaseNow(ctx)
	if err != nil {
		fail("data_retention_policies", RetentionStepLoad, fmt.Errorf("read the database clock: %w", err))
		return outcome()
	}

	policies, err := s.store.GetRetentionPolicies(ctx)
	if err != nil {
		fail("data_retention_policies", RetentionStepLoad, err)
	}
	// Until a committed delivery proves otherwise the journal is the audit
	// entry's only copy. This fixed window is independent of audit_events policy
	// and applies only when the warehouse is the store of record.
	if s.auditReads != nil && s.auditReads.Backend() == AuditBackendWarehouse {
		var removed, oldUnproven int64
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			removed, oldUnproven, err = s.store.DeleteDeliveredAuditJournal(ctx, now.UTC().AddDate(0, 0, -30))
			return err
		}); err != nil {
			fail("audit_journal", RetentionStepRemove, err)
		} else {
			deleted["audit_journal"] = removed
			w.Info("retention: audit journal delivery coverage",
				wool.Field("deleted", removed), wool.Field("old_unproven", oldUnproven), wool.Field("retention_days", 30))
		}
	}
	for _, p := range policies {
		before, anyOlder, windowErr := retentionCutoff(now, p.RetentionDays)
		if windowErr != nil {
			fail(p.ResourceType, RetentionStepPolicy, windowErr)
			continue
		}

		var count int64
		var removeErr error
		switch {
		case p.ResourceType == "audit_events" && s.auditReads != nil && s.auditReads.Backend() == AuditBackendWarehouse:
			// Under the warehouse backend audit_events is no longer the store of
			// record and nothing writes it, so this policy has nothing to retire
			// there. How long the warehouse keeps an entry is the warehouse's own
			// expiry, set where its table is provisioned, not a partition drop
			// here. Rows written before the switch are left where they are.
			w.Info("retention: audit_events policy skipped: the audit backend is warehouse, whose expiry governs retention; audit_events is not written and is left as it is",
				wool.Field("retention_days", p.RetentionDays))
			continue
		case !anyOlder:
			// Nothing is older than the cutoff.
		case p.ResourceType == "audit_events":
			// audit_events is append-only and range-partitioned by created_at:
			// retention drops whole partitions older than the window (DDL that
			// sidesteps the immutability trigger) rather than issuing a row
			// DELETE the trigger would reject. The drop decides which partitions
			// are due once it holds its lock, from the catalog as it stands then,
			// which takes a transaction of its own: the store opens one at READ
			// COMMITTED, outside the one that provisioned partitions above.
			count, removeErr = s.store.DropAuditPartitionsBefore(ctx, before)
		default:
			removeErr = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
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

		if removeErr != nil {
			fail(p.ResourceType, RetentionStepRemove, removeErr)
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
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			webauthn, mfaLogin, clientCodes, err := ceremonyStore.DeleteExpiredAuthenticationCeremonies(ctx, now.Add(-24*time.Hour))
			deleted["webauthn_ceremonies"] = webauthn
			deleted["mfa_login_transactions"] = mfaLogin
			deleted["client_authorization_codes"] = clientCodes
			return err
		}); err != nil {
			fail("authentication_ceremonies", RetentionStepRemove, err)
		}
	}

	// A GitHub App setup row is the same kind of state: a short-lived hand-off
	// that is dead once redeemed or lapsed, and that nothing else deletes. Swept
	// on the same forensic window so an abandoned connect attempt does not leave
	// a row behind permanently.
	if setupStore, ok := s.store.(githubAppSetupRetentionStore); ok {
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			setups, err := setupStore.DeleteExpiredGitHubAppSetups(ctx, now.Add(-24*time.Hour))
			deleted["github_app_setups"] = setups
			return err
		}); err != nil {
			fail("github_app_setups", RetentionStepRemove, err)
		}
	}

	return outcome()
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
