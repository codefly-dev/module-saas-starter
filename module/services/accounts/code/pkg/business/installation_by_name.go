package business

import (
	"context"
	"errors"
	"fmt"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ErrInstallTargetNotPresent reports that no target an organisation may install
// answers to the name asked: the composition has not delivered it to this host
// yet, or delivered it and withdrew it. A caller that waits on delivery (the
// fixture's declared installations) asks again after the next pass; any other
// caller hears it as a precondition.
var ErrInstallTargetNotPresent = errors.New("no installable target answers to that name")

// InstallTargetByName installs, as actorID, the target an organisation's
// catalogue offers under the name target, holding the organisation's role named
// role, with ownerID as the accountable person (actorID when empty). It is the
// resolver the host interface's Install names its arguments by: the target by
// the name the composition gives it (the presence's route alias) and the role
// by its name in the organisation's catalogue.
//
// It resolves and nothing more: the catalogue is the one ListAvailableSolutions
// serves, so nothing is installable here that an administrator could not pick,
// and the install is InstallSolution, with every check it makes. An
// organisation that already holds an active installation of the target gets
// that installation back, unchanged (installed is false), so a caller that
// declares its installations converges on them. Held is what the catalogue's
// `installed` means: any installation not revoked, a pending one among them.
func (s *Service) InstallTargetByName(ctx context.Context, actorID, orgID, target, role, ownerID string) (installation *gen.Installation, installed bool, err error) {
	available, err := s.availableTargetNamed(ctx, orgID, target)
	if err != nil {
		return nil, false, err
	}
	if available.Installed {
		existing, err := s.heldTargetInstallation(ctx, orgID, available.TargetID)
		return existing, false, err
	}
	roleID, err := s.organizationRoleNamed(ctx, orgID, role)
	if err != nil {
		return nil, false, err
	}
	installation, err = s.InstallSolution(ctx, actorID, &InstallSolutionParams{
		OrgID:            orgID,
		AgentIdentifier:  available.ReleasePublisher + "/" + available.ReleaseName + ":" + available.ReleaseVersion,
		TargetID:         available.TargetID,
		DisplayName:      available.RouteAlias,
		RootScopeLabel:   available.RouteAlias,
		RoleID:           roleID,
		OwnerPrincipalID: ownerID,
	})
	if err != nil {
		return nil, false, err
	}
	return installation, true, nil
}

// availableTargetNamed is the organisation's catalogue entry whose route alias
// is target.
func (s *Service) availableTargetNamed(ctx context.Context, orgID, target string) (*AvailableSolutionTarget, error) {
	token := ""
	for page := 0; page < activeTargetInstallationPageLimit; page++ {
		available, next, err := s.ListAvailableSolutions(ctx, orgID, token, listInstallationsMaxPageSize)
		if err != nil {
			return nil, err
		}
		for _, candidate := range available {
			if candidate.RouteAlias == target {
				return candidate, nil
			}
		}
		if next == "" {
			return nil, fmt.Errorf("%w: organization %s is offered no target named %q", ErrInstallTargetNotPresent, orgID, target)
		}
		token = next
	}
	return nil, fmt.Errorf("the catalogue of organization %s did not end within %d pages", orgID, activeTargetInstallationPageLimit)
}

// heldTargetInstallation is the organisation's installation of targetID that is
// not revoked, read through ListInstallations page by page.
func (s *Service) heldTargetInstallation(ctx context.Context, orgID, targetID string) (*gen.Installation, error) {
	token := ""
	for page := 0; page < activeTargetInstallationPageLimit; page++ {
		resp, err := s.ListInstallations(ctx, &gen.ListInstallationsRequest{OrgId: orgID, PageSize: listInstallationsMaxPageSize, PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, summary := range resp.GetInstallations() {
			installation := summary.GetInstallation()
			if installation.GetTargetId() == targetID && installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_REVOKED {
				return installation, nil
			}
		}
		if token = resp.GetNextPageToken(); token == "" {
			return nil, fmt.Errorf("the catalogue says organization %s holds target %s, and its installations hold none that is not revoked", orgID, targetID)
		}
	}
	return nil, fmt.Errorf("installations of organization %s did not end within %d pages", orgID, activeTargetInstallationPageLimit)
}

// organizationRoleNamed is the id of the role the organisation's catalogue
// lists under name: its own roles and the built-in ones.
func (s *Service) organizationRoleNamed(ctx context.Context, orgID, name string) (string, error) {
	roles, err := s.ListRoles(ctx, &gen.ListRolesRequest{OrgId: orgID})
	if err != nil {
		return "", err
	}
	found := ""
	for _, role := range roles.GetRoles() {
		if role.GetName() != name {
			continue
		}
		if found != "" {
			return "", NewStoreError(fmt.Errorf("organization %s lists two roles named %q (%s and %s): a name chooses neither", orgID, name, found, role.GetId()), ErrTypeConflict)
		}
		found = role.GetId()
	}
	if found == "" {
		return "", NewStoreError(fmt.Errorf("organization %s has no role named %q", orgID, name), ErrTypeNotFound)
	}
	return found, nil
}
