package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"
)

// OrgSettings is the domain representation of organization branding settings.
type OrgSettings struct {
	OrgID        string
	LogoURL      string
	PrimaryColor string
	CustomDomain string
	FaviconURL   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GetOrgSettings returns the branding settings for an organization.
// Returns default (empty) settings if none have been configured yet.
// org_settings is RLS-protected (Phase 2B); read inside WithOrgTx.
func (s *Service) GetOrgSettings(ctx context.Context, orgID string) (*OrgSettings, error) {
	w := wool.Get(ctx).In("GetOrgSettings")

	var settings *OrgSettings
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		s, err := s.store.GetOrgSettings(ctx, orgID)
		settings = s
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get org settings")
	}
	if settings == nil {
		return &OrgSettings{OrgID: orgID}, nil
	}
	return settings, nil
}

// UpdateOrgSettings creates or updates the branding settings for an organization.
func (s *Service) UpdateOrgSettings(ctx context.Context, actorID string, settings *OrgSettings) (*OrgSettings, error) {
	w := wool.Get(ctx).In("UpdateOrgSettings")

	var fresh *OrgSettings
	if err := s.store.WithOrgTx(ctx, settings.OrgID, func(ctx context.Context) error {
		if err := s.store.UpsertOrgSettings(ctx, settings); err != nil {
			return err
		}
		f, err := s.store.GetOrgSettings(ctx, settings.OrgID)
		fresh = f
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot update org settings")
	}

	s.emit(ctx, actorID, "user", EventOrgSettingsUpdated, "organization", settings.OrgID, settings.OrgID)
	return fresh, nil
}
