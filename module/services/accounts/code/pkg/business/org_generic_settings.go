package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/orgsettings"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetOrgGenericSettings returns an organization's generic typed settings
// document. JSON is confined to the Postgres adapter; business and transport
// code share this one canonical type. org_generic_settings is RLS-protected;
// read inside WithOrgTx.
func (s *Service) GetOrgGenericSettings(ctx context.Context, orgID string) (*gen.OrganizationSettings, error) {
	w := wool.Get(ctx).In("GetOrgGenericSettings")

	var settings *gen.OrganizationSettings
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		settings, err = s.store.GetOrgGenericSettings(ctx, orgID)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get org generic settings")
	}
	resolved, err := orgsettings.Resolve(settings)
	if err != nil {
		return nil, status.Error(codes.Internal, "resolve org settings")
	}
	return resolved, nil
}

// UpdateOrgGenericSettings deep-merges a typed partial protobuf document under
// the org row lock. Optional scalar presence makes explicit false/empty values
// different from "leave this setting unchanged"; nested message merge and reset
// pruning are performed atomically by the Postgres adapter.
func (s *Service) UpdateOrgGenericSettings(
	ctx context.Context,
	actorID string,
	orgID string,
	patch *gen.OrganizationSettings,
	resetPaths []string,
) (*gen.OrganizationSettings, error) {
	w := wool.Get(ctx).In("UpdateOrgGenericSettings")

	if patch == nil && len(resetPaths) == 0 {
		return s.GetOrgGenericSettings(ctx, orgID)
	}
	if patch == nil {
		patch = &gen.OrganizationSettings{}
	}
	if err := orgsettings.ValidateResetPaths(patch, resetPaths); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var settings *gen.OrganizationSettings
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.store.UpdateOrgGenericSettings(ctx, orgID, patch, resetPaths); err != nil {
			return err
		}
		var err error
		settings, err = s.store.GetOrgGenericSettings(ctx, orgID)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot update org generic settings")
	}
	s.emit(ctx, actorID, "user", EventOrgGenericSettingsUpdated, "organization", orgID, orgID)
	resolved, err := orgsettings.Resolve(settings)
	if err != nil {
		return nil, status.Error(codes.Internal, "resolve org settings")
	}
	return resolved, nil
}
