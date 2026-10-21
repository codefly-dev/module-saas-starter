package fixtures

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Installation fixtures declare consent for an exact release, never an alias
// alone. They submit the ordinary installation operation; the existing host
// worker owns acknowledgement, retries and activation.
type fixtureInstallation struct {
	Org              string                    `yaml:"org"`
	Installer        string                    `yaml:"installer"`
	Publisher        string                    `yaml:"publisher"`
	Name             string                    `yaml:"name"`
	Version          string                    `yaml:"version"`
	Role             string                    `yaml:"role"`
	AllowedAudiences []string                  `yaml:"allowed_audiences"`
	AllowedScopes    []string                  `yaml:"allowed_scopes"`
	Teams            []fixtureInstallationTeam `yaml:"teams"`
}

type fixtureInstallationTeam struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"`
}

func validateInstallations(f *fixtureFile) error {
	seen := map[string]bool{}
	for _, install := range f.Installations {
		key := fixtureScopedKey(install.Org, install.Publisher+"/"+install.Name)
		if seen[key] || install.Publisher == "" || install.Name == "" || install.Version == "" {
			return fmt.Errorf("installation requires a unique organization and exact publisher/name/version")
		}
		seen[key] = true
		var org fixtureOrg
		for _, candidate := range f.Organizations {
			if candidate.Name == install.Org {
				org = candidate
			}
		}
		var user fixtureUser
		for _, candidate := range f.Users {
			if candidate.Email == install.Installer {
				user = candidate
			}
		}
		admin := org.Owner == install.Installer
		for _, member := range org.Members {
			if member.Email == install.Installer && (member.Role == "admin" || member.Role == "owner") {
				admin = true
			}
		}
		if org.ID == "" || user.ID == "" || !admin {
			return fmt.Errorf("installation %s requires a fixture organization and its administrator with pinned ids", install.Name)
		}
		roleExists := func(name string) bool {
			for _, role := range f.Roles {
				if role.Org == install.Org && role.Name == name {
					return true
				}
			}
			return false
		}
		if !roleExists(install.Role) {
			return fmt.Errorf("installation %s references an undeclared role", install.Name)
		}
		for _, grant := range install.Teams {
			found := false
			for _, team := range f.Teams {
				if team.Org == install.Org && team.Name == grant.Name {
					found = true
				}
			}
			if !found || !roleExists(grant.Role) {
				return fmt.Errorf("installation %s references an undeclared team or grant role", install.Name)
			}
		}
	}
	return nil
}

type installationService interface {
	ListAvailableSolutions(context.Context, string, string, int) ([]*business.AvailableSolutionTarget, string, error)
	ListRoles(context.Context, *gen.ListRolesRequest) (*gen.ListRolesResponse, error)
	ListTeams(context.Context, *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error)
	ListPrincipals(context.Context, string, string, int32, string) ([]*business.Principal, string, error)
	ListInstallations(context.Context, *gen.ListInstallationsRequest) (*gen.ListInstallationsResponse, error)
	InstallSolution(context.Context, string, *business.InstallSolutionParams) (*gen.Installation, error)
	GetInstallation(context.Context, string, string) (*gen.Installation, gen.InstallationHealth, error)
	GrantInstallationToTeam(context.Context, string, string, string, string, string) (*business.InstallationGrant, error)
}

// PrepareInstallations validates explicitly selected local setup before any
// fixture writes. The returned application runs after listeners are available.
func PrepareInstallations(service *business.Service, name string, local bool) (func(context.Context) error, error) {
	path, err := FixturePath(name)
	if err != nil {
		return nil, err
	}
	f, err := loadFixtureFile(path)
	if err != nil {
		return nil, err
	}
	if len(f.Installations) > 0 && !local {
		return nil, fmt.Errorf("installation fixtures are only permitted in the local environment")
	}
	return func(ctx context.Context) error {
		if err := validateInstallationActors(ctx, f, func(ctx context.Context, user fixtureUser) (*gen.User, error) {
			var stored *gen.User
			err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
				var err error
				stored, err = service.Store().GetUserByIdentity(ctx, &gen.UserIdentity{Provider: user.Provider, ProviderId: user.ProviderID})
				return err
			})
			return stored, err
		}); err != nil {
			return err
		}
		return applyInstallations(ctx, service, f, time.Second)
	}, nil
}

// Legacy seeds may retain a different UUID. Never attribute new consent to the
// fixture's declared ID unless the actual provider identity resolves to it.
func validateInstallationActors(ctx context.Context, f *fixtureFile, lookup func(context.Context, fixtureUser) (*gen.User, error)) error {
	for _, install := range f.Installations {
		for _, user := range f.Users {
			if user.Email != install.Installer {
				continue
			}
			stored, err := lookup(ctx, user)
			if err != nil {
				return err
			}
			if stored == nil || stored.Uuid != user.ID {
				return fmt.Errorf("installation %s installer identity differs from its declared fixture id; use compatible fixture state", install.Name)
			}
		}
	}
	return nil
}

func applyInstallations(ctx context.Context, service installationService, f *fixtureFile, interval time.Duration) error {
	for _, install := range f.Installations {
		var orgID, actorID string
		for _, org := range f.Organizations {
			if org.Name == install.Org {
				orgID = org.ID
			}
		}
		for _, user := range f.Users {
			if user.Email == install.Installer {
				actorID = user.ID
			}
		}
		roles, err := service.ListRoles(ctx, &gen.ListRolesRequest{OrgId: orgID})
		if err != nil {
			return err
		}
		roleID := func(name string) (string, error) {
			var id string
			for _, role := range roles.Roles {
				if role.OrgId == orgID && role.Name == name && !role.BuiltIn {
					if id != "" {
						return "", fmt.Errorf("ambiguous fixture role %q", name)
					}
					id = role.Id
				}
			}
			if id == "" {
				return "", fmt.Errorf("missing fixture role %q", name)
			}
			return id, nil
		}
		role, err := roleID(install.Role)
		if err != nil {
			return err
		}
		var target *business.AvailableSolutionTarget
		err = waitFixture(ctx, interval, func() (bool, error) {
			var matched *business.AvailableSolutionTarget
			var cursor string
			for {
				page, next, err := service.ListAvailableSolutions(ctx, orgID, cursor, 100)
				if errors.Is(err, business.ErrModuleRegistryStale) {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				for _, candidate := range page {
					if candidate.ReleasePublisher == install.Publisher && candidate.ReleaseName == install.Name && candidate.ReleaseVersion == install.Version {
						if matched != nil {
							return false, fmt.Errorf("ambiguous accepted release %s/%s:%s", install.Publisher, install.Name, install.Version)
						}
						matched = candidate
					}
				}
				if next == "" {
					break
				}
				if next == cursor {
					return false, fmt.Errorf("repeated installation target cursor")
				}
				cursor = next
			}
			target = matched
			return target != nil, nil
		})
		if err != nil {
			return fmt.Errorf("fixture release %s/%s:%s is not available: %w", install.Publisher, install.Name, install.Version, err)
		}
		if err := refuseFixtureRetarget(ctx, service, orgID, install, target.TargetID, interval); err != nil {
			return err
		}
		installed, err := service.InstallSolution(ctx, actorID, &business.InstallSolutionParams{
			OrgID: orgID, TargetID: target.TargetID, AgentIdentifier: install.Publisher + "/" + install.Name + ":" + install.Version,
			RoleID: role, DisplayName: install.Name, RootScopeLabel: install.Name,
			AllowedAudiences: install.AllowedAudiences, AllowedScopes: install.AllowedScopes,
		})
		if err != nil {
			return fmt.Errorf("fixture installation %s refused: %w", install.Name, err)
		}
		if err := awaitFixtureInstallation(ctx, service, orgID, installed.Id, interval); err != nil {
			return fmt.Errorf("fixture installation %s did not acknowledge: %w", install.Name, err)
		}
		teams, err := service.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgID})
		if err != nil {
			return err
		}
		for _, grant := range install.Teams {
			var teamID string
			for _, team := range teams.Teams {
				if team.Name == grant.Name {
					if teamID != "" {
						return fmt.Errorf("ambiguous fixture team %q", grant.Name)
					}
					teamID = team.Id
				}
			}
			if teamID == "" {
				return fmt.Errorf("missing fixture team %q", grant.Name)
			}
			role, err := roleID(grant.Role)
			if err != nil {
				return err
			}
			if _, err := service.GrantInstallationToTeam(ctx, actorID, orgID, installed.Id, teamID, role); err != nil {
				return err
			}
		}
	}
	return nil
}

// A repeated fixture is not consent to a replacement delivery or an uninstall
// reversal. Read retained installation/principal identities through the owner's
// existing APIs; the install operation checks all remaining consent equality.
func refuseFixtureRetarget(ctx context.Context, service installationService, orgID string, install fixtureInstallation, targetID string, interval time.Duration) error {
	principals := map[string]bool{}
	var cursor string
	for {
		page, next, err := service.ListPrincipals(ctx, orgID, "agent", 100, cursor)
		if err != nil {
			return err
		}
		for _, principal := range page {
			if strings.HasPrefix(principal.AgentIdentifier, install.Publisher+"/"+install.Name+":") {
				principals[principal.ID] = true
			}
		}
		if next == "" {
			break
		}
		if next == cursor {
			return fmt.Errorf("repeated principal cursor")
		}
		cursor = next
	}
	cursor = ""
	for {
		page, err := service.ListInstallations(ctx, &gen.ListInstallationsRequest{OrgId: orgID, PageSize: 100, PageToken: cursor})
		if err != nil {
			return err
		}
		for _, row := range page.Installations {
			existing := row.Installation
			if existing.TargetId == targetID && existing.Status == gen.InstallationStatus_INSTALLATION_STATUS_PENDING {
				// Observation only. Full consent equality is checked by the ordinary
				// install operation once the existing worker has acknowledged it.
				if err := awaitFixtureInstallation(ctx, service, orgID, existing.Id, interval); err != nil {
					return err
				}
			}
			if principals[existing.AgentPrincipalId] && (existing.TargetId != targetID || existing.Status == gen.InstallationStatus_INSTALLATION_STATUS_REVOKED) {
				return fmt.Errorf("fixture %s refuses retained installation replacement or revocation reversal; use fresh fixture state", install.Name)
			}
		}
		if page.NextPageToken == "" {
			return nil
		}
		if page.NextPageToken == cursor {
			return fmt.Errorf("repeated installation cursor")
		}
		cursor = page.NextPageToken
	}
}

func waitFixture(ctx context.Context, interval time.Duration, check func() (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := check()
		if err != nil || ready {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func awaitFixtureInstallation(ctx context.Context, service installationService, orgID, id string, interval time.Duration) error {
	return waitFixture(ctx, interval, func() (bool, error) {
		current, health, err := service.GetInstallation(ctx, orgID, id)
		if err != nil {
			return false, err
		}
		switch current.Status {
		case gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE:
			if health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY {
				return false, fmt.Errorf("active installation is unhealthy")
			}
			return true, nil
		case gen.InstallationStatus_INSTALLATION_STATUS_PENDING:
			return false, nil
		default:
			return false, fmt.Errorf("installation status %s", current.Status)
		}
	})
}
