//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// The person-facing approval review against a live Postgres: who may see a
// request, the tenant boundary, the reviewed-subject binding and separation of
// duties — each proven to leave no decision row behind when it refuses — and
// the audit record every accepted decision commits with.

type reviewFixture struct {
	svc       *business.Service
	org       string
	id        string
	requester string
	approvers []string
	subject   map[string]any
	hash      string
}

func newReviewFixture(t *testing.T, quorum int, approvers int, allowSelf bool) reviewFixture {
	t.Helper()
	requester := seedUser(t)
	org := seedOrg(t, requester)
	f := reviewFixture{org: org, requester: requester}
	for i := 0; i < approvers; i++ {
		f.approvers = append(f.approvers, seedUser(t))
	}
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	f.svc = auditedService(t, emitter)
	f.subject = map[string]any{"reference": "urn:example:revision:" + business.NewIDString(), "amount": 42.0}
	f.hash, err = business.ApprovalSubjectHash(f.subject)
	require.NoError(t, err)
	f.id, err = f.svc.CreateApprovalRequest(testCtx, &business.CreateApprovalRequestInput{
		OrgID: org, Resource: "example", Action: "advance", RequestedBy: requester,
		Subject: f.subject, Quorum: quorum,
		Policy: business.ApprovalPolicy{ApproverSet: f.approvers, AllowSelf: allowSelf},
	})
	require.NoError(t, err)
	return f
}

func (f reviewFixture) decide(actor string, decision business.ApprovalDecisionKind, hash string) (*business.ApprovalReview, error) {
	return f.svc.DecideReviewedApproval(testCtx, f.org, f.id, business.DecideInput{
		Decider: actor, Decision: decision, ExpectedSubjectHash: hash,
	})
}

func decisionRows(t *testing.T, requestID string) int {
	t.Helper()
	return countRows(t, `SELECT count(*) FROM approval_decisions WHERE request_id = $1`, requestID)
}

func TestApprovalReview_VisibleToRequesterAndApproversOnly(t *testing.T) {
	f := newReviewFixture(t, 1, 2, false)
	for _, actor := range append([]string{f.requester}, f.approvers...) {
		review, err := f.svc.ReviewApproval(testCtx, f.org, f.id, actor)
		require.NoError(t, err)
		require.Equal(t, f.hash, review.SubjectHash, "the hash is computed over the subject as stored")
		require.Equal(t, f.subject, review.Request.Subject)
		require.Empty(t, review.Decisions)
	}

	bystander := seedUser(t)
	seedOrgMember(t, f.org, bystander)
	_, err := f.svc.ReviewApproval(testCtx, f.org, f.id, bystander)
	require.Equal(t, business.ErrTypeNotFound, storeErrTypeInfra(t, err), "an org member the request does not name cannot see it")
	_, err = f.decide(bystander, business.DecisionApprove, f.hash)
	require.Equal(t, business.ErrTypeNotFound, storeErrTypeInfra(t, err))
	require.Zero(t, decisionRows(t, f.id))
}

func TestApprovalReview_CrossOrganizationIsInvisible(t *testing.T) {
	f := newReviewFixture(t, 1, 1, false)
	other := seedOrg(t, f.approvers[0])

	// The approver is named on the request, but asks under another tenant: RLS
	// keys on that tenant, so the request does not exist there.
	_, err := f.svc.ReviewApproval(testCtx, other, f.id, f.approvers[0])
	require.Equal(t, business.ErrTypeNotFound, storeErrTypeInfra(t, err))
	_, err = f.svc.DecideReviewedApproval(testCtx, other, f.id, business.DecideInput{
		Decider: f.approvers[0], Decision: business.DecisionApprove, ExpectedSubjectHash: f.hash,
	})
	require.Equal(t, business.ErrTypeNotFound, storeErrTypeInfra(t, err))
	require.Zero(t, decisionRows(t, f.id))

	// Decisions are tenant-scoped as well.
	_, err = f.decide(f.approvers[0], business.DecisionApprove, f.hash)
	require.NoError(t, err)
	var leaked []business.ApprovalDecision
	require.NoError(t, testStore.As(business.Identity{OrgID: other}).Within(testCtx, func(ctx context.Context) error {
		var e error
		leaked, e = testStore.ListApprovalDecisions(ctx, f.id, f.org)
		return e
	}))
	require.Empty(t, leaked, "RLS hides another tenant's decisions even when the query names that tenant")
}

func TestApprovalReview_StaleSubjectHashRecordsNothing(t *testing.T) {
	f := newReviewFixture(t, 1, 1, false)
	stale, err := business.ApprovalSubjectHash(map[string]any{"reference": "urn:example:revision:0"})
	require.NoError(t, err)

	_, err = f.decide(f.approvers[0], business.DecisionApprove, stale)
	require.Equal(t, business.ErrTypeConflict, storeErrTypeInfra(t, err))
	require.Zero(t, decisionRows(t, f.id))
	require.Zero(t, countAuditEvents(t, string(business.EventApprovalDecisionRecorded), f.id))

	review, err := f.decide(f.approvers[0], business.DecisionApprove, f.hash)
	require.NoError(t, err)
	require.Equal(t, business.ApprovalApproved, review.Request.State)
}

func TestApprovalReview_RequesterCannotApproveOwnRequest(t *testing.T) {
	f := newReviewFixture(t, 1, 1, false)
	_, err := f.decide(f.requester, business.DecisionApprove, f.hash)
	require.Equal(t, business.ErrTypePermission, storeErrTypeInfra(t, err))
	require.Zero(t, decisionRows(t, f.id))

	// Opting in to self-approval is the request's choice, not the caller's. With
	// no approver set the request names its requester alone.
	g := newReviewFixture(t, 1, 0, true)
	review, err := g.decide(g.requester, business.DecisionApprove, g.hash)
	require.NoError(t, err)
	require.Equal(t, business.ApprovalApproved, review.Request.State)
}

func TestApprovalReview_DecisionsAreAuditedAndReturned(t *testing.T) {
	f := newReviewFixture(t, 2, 2, false)

	review, err := f.decide(f.approvers[0], business.DecisionApprove, f.hash)
	require.NoError(t, err)
	require.Equal(t, business.ApprovalPending, review.Request.State)
	require.Len(t, review.Decisions, 1)
	require.Equal(t, f.approvers[0], review.Decisions[0].Decider)
	require.Equal(t, 1, countAuditEvents(t, string(business.EventApprovalDecisionRecorded), f.id))
	require.Zero(t, countAuditEvents(t, string(business.EventApprovalApproved), f.id))

	_, err = f.decide(f.approvers[0], business.DecisionApprove, f.hash)
	require.Equal(t, business.ErrTypeConflict, storeErrTypeInfra(t, err), "one vote per approver")

	review, err = f.decide(f.approvers[1], business.DecisionApprove, f.hash)
	require.NoError(t, err)
	require.Equal(t, business.ApprovalApproved, review.Request.State)
	require.Len(t, review.Decisions, 2)
	require.Equal(t, 2, countAuditEvents(t, string(business.EventApprovalDecisionRecorded), f.id))
	require.Equal(t, 1, countAuditEvents(t, string(business.EventApprovalApproved), f.id))

	// A closed request stays readable to the people it named.
	got, err := f.svc.ReviewApproval(testCtx, f.org, f.id, f.requester)
	require.NoError(t, err)
	require.Len(t, got.Decisions, 2)
}
