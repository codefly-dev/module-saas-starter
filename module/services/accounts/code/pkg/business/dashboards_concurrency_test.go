package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// deletedMidWriteStore simulates the READ COMMITTED race where a row is present
// at the in-transaction load but a concurrent delete removes it before the
// mutating UPDATE, so UpdateDashboard/SetDashboardVisibility see a zero-row
// RETURNING and return (nil, nil).
type deletedMidWriteStore struct {
	business.Store
	record *business.Dashboard
}

func (s *deletedMidWriteStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *deletedMidWriteStore) GetDashboard(context.Context, string) (*business.Dashboard, error) {
	return s.record, nil
}

func (s *deletedMidWriteStore) UpdateDashboard(context.Context, string, *string, []byte) (*business.Dashboard, error) {
	return nil, nil
}

func (s *deletedMidWriteStore) SetDashboardVisibility(context.Context, string, business.DashboardVisibility) (*business.Dashboard, error) {
	return nil, nil
}

// A row deleted between the load and the RETURNING update must surface as
// NotFound, never as a nil record — the handler dereferences the returned
// record and would panic on nil.
func TestDashboards_UpdateOnConcurrentlyDeletedRowIsNotFound(t *testing.T) {
	owner := business.NewIDString()
	store := &deletedMidWriteStore{record: &business.Dashboard{
		ID: "d1", OrgID: "o1", OwnerID: owner, Visibility: business.DashboardVisibilityPrivate,
	}}
	svc, err := business.NewService(store)
	require.NoError(t, err)

	name := "renamed"
	got, err := svc.UpdateDashboard(context.Background(), "o1", owner, false, "d1", &name, nil)
	require.Nil(t, got)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestDashboards_ShareOnConcurrentlyDeletedRowIsNotFound(t *testing.T) {
	owner := business.NewIDString()
	store := &deletedMidWriteStore{record: &business.Dashboard{
		ID: "d1", OrgID: "o1", OwnerID: owner, Visibility: business.DashboardVisibilityPrivate,
	}}
	svc, err := business.NewService(store)
	require.NoError(t, err)

	got, err := svc.ShareDashboard(context.Background(), "o1", owner, "d1", business.DashboardVisibilityOrg)
	require.Nil(t, got)
	require.Equal(t, codes.NotFound, status.Code(err))
}
