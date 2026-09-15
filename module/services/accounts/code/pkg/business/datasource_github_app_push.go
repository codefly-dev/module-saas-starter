package business

// The App-level content seam (issue #734).
//
// A GitHub App has exactly one webhook URL and one webhook secret, so a
// tenant's `push` events arrive App-wide, naming an installation and a
// repository but no source and no tenant. An App-backed source has no push
// secret of its own, so the per-source receiver can never verify a push for it;
// this is the path that gives those sources live content delivery.
//
// Receipt does not fan out. It durably accepts one delivery and returns 2xx
// (pkg/datasource, GitHubAppPushTopic); this leased worker then resolves who is
// currently eligible and enqueues one ordinary per-source push delivery each.
// So a fan-out interrupted halfway is retried from the durable delivery rather
// than lost with the request, and every source reached is compiled by the
// existing change-set compiler with no second content path.
//
// Nothing here reads the push body. Which sources a delivery concerns comes
// from the installation and repository the receiver verified, and each source's
// tenant, boundary, branch and path scope come from its own row — never from
// the payload, which is a third party's claim about what changed.

import (
	"context"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
)

const (
	// datasourceAppPushTopic must equal the receiver's GitHubAppPushTopic; the
	// delivery worker dispatches on it. That package imports this one, so the
	// constant cannot be shared without an import cycle.
	datasourceAppPushTopic = "datasource.github.app_push"

	// datasourceAppPushSource marks the per-source deliveries this fan-out
	// produces, as distinct from the ones a source's own webhook produced. Both
	// carry the same topic and are compiled identically; only their provenance
	// differs.
	datasourceAppPushSource = "github.app.push"

	// datasourcePushSchemaVersion versions the raw push envelope a per-source
	// delivery carries. It must equal the receiver's GitHubWebhookSchemaVersion:
	// the fan-out enqueues onto that same topic, so one consumer must not see a
	// different envelope depending on which producer it came from.
	datasourcePushSchemaVersion = 1

	// datasourceAppPushPageSize bounds one read of the sources a push concerns.
	// One installation can serve the same repository in several tenants, so the
	// fan-out pages rather than holding the whole set.
	datasourceAppPushPageSize = 100
)

// GitHubAppPushDeliveryKey is the durable identity of one fanned-out delivery:
// the original GitHub delivery id, which is stable across redelivery, paired
// with the source it was fanned out to.
//
// Both halves are load-bearing. The delivery id alone would let the first
// source's job absorb every other source's (one key, one job), and a fresh key
// per attempt would re-enqueue work a retried fan-out had already delivered.
// Keyed this way, a redelivered push, a retried fan-out and a replaced worker
// all resolve to the job already recorded, so no logical change is duplicated
// or dropped.
func GitHubAppPushDeliveryKey(deliveryID, sourceID string) string {
	return deliveryID + ":" + sourceID
}

// handleGitHubAppPushJob dispatches one durably accepted App-level content
// delivery to the fan-out. The routing facts are read from the job's attributes
// — the receiver verified them against the App's signature — and a job carrying
// neither will not carry them on redelivery either.
func (s *Service) handleGitHubAppPushJob(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
	installationID := envelope.GetAttributes()[attrInstallationID]
	repo := envelope.GetAttributes()[attrRepo]
	if installationID == "" || repo == "" {
		return jobs.NewProcessingError("datasource.invalid_job",
			"datasource app push job names no installation or repository", false)
	}
	_, err := s.FanOutGitHubAppPush(ctx, installationID, repo,
		envelope.GetAttributes()[attrDeliveryID], envelope.GetPayload())
	return err
}

// FanOutGitHubAppPush enqueues one per-source push delivery for every source
// the installation currently grants this repository to, and reports how many it
// reached.
//
// Eligibility is re-read on every delivery rather than cached: the listing
// returns only active sources bound to this installation and repository, so a
// suspended installation, a deselected repository, an operator pause, a
// compiler degrade and a disconnected source are all excluded by the time the
// next push arrives — and the source's credential envelope, which is the sole
// authority a content fetch mints a token from, refuses the fetch anyway once
// GitHub has revoked access. An installation serving several tenants fans out
// to each of their sources separately; no global tenant is inferred from the
// delivery.
func (s *Service) FanOutGitHubAppPush(ctx context.Context, installationID, repo, deliveryID string, delivery []byte) (int, error) {
	w := wool.Get(ctx).In("FanOutGitHubAppPush")
	if s.datasourceJobs == nil {
		return 0, w.NewError("datasource connector is not configured")
	}
	var failure error
	fanned := 0
	after := ""
	for {
		var page []*DatasourceSource
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			page, err = s.store.ListActiveDatasourceSourcesByGitHubInstallationRepo(ctx,
				installationID, repo, after, datasourceAppPushPageSize)
			return err
		}); err != nil {
			return fanned, w.Wrapf(err, "list eligible sources")
		}
		for _, source := range page {
			after = source.ID
			if err := s.enqueueAppPushDelivery(ctx, source, deliveryID, delivery); err != nil {
				// One source's enqueue failing must not strand the others,
				// which may belong to other tenants: record it, keep going, and
				// let the retry re-walk the installation. The already-enqueued
				// jobs resolve to themselves on that walk, so recovery neither
				// duplicates nor skips.
				w.Warn("fan out app push failed", wool.Field("source", source.ID), wool.ErrField(err))
				failure = keepRetryable(failure, err)
				continue
			}
			fanned++
		}
		if len(page) < datasourceAppPushPageSize {
			return fanned, failure
		}
	}
}

// enqueueAppPushDelivery records one source's copy of an App-level push as an
// ordinary per-source delivery: the same queue, topic and per-source FIFO
// ordering key a source's own webhook produces, so the compiler applies the
// same branch filter, cursor ancestry guard and snapshot fallback with no
// knowledge of where the delivery entered.
func (s *Service) enqueueAppPushDelivery(ctx context.Context, source *DatasourceSource, deliveryID string, delivery []byte) error {
	_, err := s.datasourceJobs.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			Queue:          DatasourceDeliveryQueue,
			Topic:          datasourcePushTopic,
			Source:         datasourceAppPushSource,
			Ordering:       DatasourceDeliveryOrderingKey(source.ID),
			IdempotencyKey: GitHubAppPushDeliveryKey(deliveryID, source.ID),
			SchemaVersion:  datasourcePushSchemaVersion,
			Payload:        delivery,
			ContentType:    datasourceRequestContentType,
			MaxAttempts:    datasourceDeliveryMaxAttempts,
			Attributes: map[string]string{
				attrSourceID:   source.ID,
				attrOrgID:      source.OrgID,
				attrBoundaryID: source.BoundaryNodeID,
				attrDeliveryID: deliveryID,
			},
		},
	})
	return err
}
