package business_test

import (
	"context"
	"sync"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type orgGenericSettingsFakeStore struct {
	business.Store
	mu       sync.Mutex
	settings map[string]*gen.OrganizationSettings
}

func newOrgGenericSettingsFakeStore() *orgGenericSettingsFakeStore {
	return &orgGenericSettingsFakeStore{settings: map[string]*gen.OrganizationSettings{}}
}

func (s *orgGenericSettingsFakeStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *orgGenericSettingsFakeStore) GetOrgGenericSettings(_ context.Context, orgID string) (*gen.OrganizationSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.settings[orgID]; v != nil {
		return proto.Clone(v).(*gen.OrganizationSettings), nil
	}
	return &gen.OrganizationSettings{}, nil
}

func (s *orgGenericSettingsFakeStore) UpdateOrgGenericSettings(_ context.Context, orgID string, patch *gen.OrganizationSettings, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.settings[orgID]
	if current == nil {
		current = &gen.OrganizationSettings{}
	}
	proto.Merge(current, patch)
	s.settings[orgID] = current
	return nil
}

// TestUpdateOrgGenericSettingsEmitsDistinctAuditEvent guards against the
// conflation the review found: the generic org-settings write must emit its own
// audit event, not the branding surface's org.settings_updated, so a compliance
// reviewer can tell the two admin operations apart.
func TestUpdateOrgGenericSettingsEmitsDistinctAuditEvent(t *testing.T) {
	svc, _ := business.NewService(newOrgGenericSettingsFakeStore())
	audit := &recordingAudit{}
	svc.SetAuditEmitter(audit)

	_, err := svc.UpdateOrgGenericSettings(context.Background(), "actor-1", testOrg, &gen.OrganizationSettings{}, nil)
	require.NoError(t, err)

	require.Equal(t, []business.EventType{business.EventOrgGenericSettingsUpdated}, audit.types())
	require.NotContains(t, audit.types(), business.EventOrgSettingsUpdated,
		"generic org-settings update must not reuse the branding audit event")
}

// A no-op update (nil patch, no resets) short-circuits to a read and must not
// emit an audit event.
func TestUpdateOrgGenericSettingsNoopDoesNotEmit(t *testing.T) {
	svc, _ := business.NewService(newOrgGenericSettingsFakeStore())
	audit := &recordingAudit{}
	svc.SetAuditEmitter(audit)

	_, err := svc.UpdateOrgGenericSettings(context.Background(), "actor-1", testOrg, nil, nil)
	require.NoError(t, err)
	require.Empty(t, audit.types())
}
