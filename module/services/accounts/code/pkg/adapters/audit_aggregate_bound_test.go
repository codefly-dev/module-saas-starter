package adapters

import (
	"context"
	"fmt"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// tooLargeAuditStore is a store of record whose aggregation is refused for its
// size, as a warehouse store's is when what it keeps passes its bound.
type tooLargeAuditStore struct{ business.AuditStore }

func (tooLargeAuditStore) AggregateAuditEvents(context.Context, business.AuditRead, business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	return []business.AuditAggregateBucket{{Key: "partial", Count: 1}}, fmt.Errorf("read: %w", business.ErrAuditAggregateTooLarge)
}

// The refusal reaches the RPC's caller as ResourceExhausted, with no bucket, on
// both paths an aggregation takes: the organization-wide read and the one gated
// on a collection grant.
func TestAggregateAuditLogRefusedForSizeIsResourceExhausted(t *testing.T) {
	store := &auditContractStore{layeredAuthzStore: layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_ADMIN}, allowed: true}
	installLayeredAuthzService(t, store)
	service.SetAuditStore(tooLargeAuditStore{})
	previousCache := orgMembershipCache
	orgMembershipCache = nil
	t.Cleanup(func() { orgMembershipCache = previousCache })
	ctx := stampVerifiedIdentity(context.Background(), layeredActorID, layeredOrgID, auth.Assurance{})

	for name, request := range map[string]*gen.AggregateAuditLogRequest{
		"organization-wide":   {OrgId: layeredOrgID},
		"a collection's read": {OrgId: layeredOrgID, CollectionId: "collection-a", EventType: "saas.document.read"},
	} {
		response, err := (&AuditServer{}).AggregateAuditLog(ctx, request)
		require.Equal(t, codes.ResourceExhausted, status.Code(err), "%s: %v", name, err)
		require.ErrorContains(t, err, "narrow it by time range, actor or event type", name)
		require.Nil(t, response, name)
	}
}
