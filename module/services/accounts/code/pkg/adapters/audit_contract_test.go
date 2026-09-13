package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type auditContractStore struct {
	layeredAuthzStore
	allowed bool
	query   business.AuditQuery
}

func (s *auditContractStore) CanReadScopeNode(_ context.Context, org, reader string, kind gen.SubjectKind, resource, action, node string) (bool, error) {
	return s.allowed && org == layeredOrgID && reader == layeredActorID && kind == gen.SubjectKind_SUBJECT_KIND_PRINCIPAL && resource == "documents" && action == "read" && node == "collection-a", nil
}

func (s *auditContractStore) AggregateAuditLog(_ context.Context, query business.AuditQuery, _ business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	s.query = query
	return nil, nil
}

func TestAuditAcknowledgesAuthorizedEmptyResponse(t *testing.T) {
	store := &auditContractStore{layeredAuthzStore: layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_ADMIN}, allowed: true}
	installLayeredAuthzService(t, store)
	previousCache := orgMembershipCache
	orgMembershipCache = nil
	t.Cleanup(func() { orgMembershipCache = previousCache })
	ctx := stampVerifiedIdentity(context.Background(), layeredActorID, layeredOrgID, auth.Assurance{})
	request := &gen.AggregateAuditLogRequest{OrgId: layeredOrgID, CollectionId: "collection-a", EventType: "saas.document.read", PayloadContains: map[string]string{"outcome": "returned"}}
	response, err := (&AuditServer{}).AggregateAuditLog(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, response.ScopeContractVersion)
	require.Empty(t, response.Buckets)
	require.Empty(t, store.query.CollectionID)
	require.Equal(t, map[string]any{"boundary": "collection-a", "outcome": "returned"}, store.query.PayloadContains)
	store.allowed = false
	response, err = (&AuditServer{}).AggregateAuditLog(ctx, request)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Nil(t, response)
}
