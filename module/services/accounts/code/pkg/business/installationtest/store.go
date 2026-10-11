// Package installationtest provides explicit installation fixtures for tests
// without PostgreSQL. It implements the same install/read seam as the store.
package installationtest

import (
	"context"
	"fmt"
	"sync"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type Store struct {
	mu   sync.Mutex
	rows map[string]*business.LiveModuleAuthority
}

func (s *Store) InstallSolution(_ context.Context, p *business.InstallSolutionParams) (*gen.Installation, error) {
	if p.OrgID == "" || p.RegistryPrincipalID == "" || p.RegistryBindingID == "" || p.Scope == nil {
		return nil, fmt.Errorf("installation fixture requires organization, registry binding, principal and scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]*business.LiveModuleAuthority{}
	}
	key := p.OrgID + "/" + p.RegistryPrincipalID
	if s.rows[key] != nil {
		return nil, fmt.Errorf("installation fixture already exists")
	}
	row := &business.LiveModuleAuthority{InstallationID: business.NewIDString(), RegistryBindingID: p.RegistryBindingID, InstallationRevision: 1, Scope: *p.Scope}
	s.rows[key] = row
	return &gen.Installation{Id: row.InstallationID, OrgId: p.OrgID, Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE}, nil
}

func (s *Store) LiveModuleAuthority(_ context.Context, principal, org, id string) (*business.LiveModuleAuthority, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[org+"/"+principal]
	if row == nil || (id != "" && row.InstallationID != id) {
		return nil, business.ErrModuleInstallationInactive
	}
	out := *row
	return &out, nil
}

func (s *Store) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
