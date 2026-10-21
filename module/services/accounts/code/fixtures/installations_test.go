package fixtures

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"gopkg.in/yaml.v3"
)

func TestInstallationFixtureIsRejectedOutsideLocalBeforeApplication(t *testing.T) {
	f := exampleInstallationFixture()
	f.Users[0].ID = "00000000-0000-7000-8000-000000000001"
	f.Users[0].Provider, f.Users[0].ProviderID = "email", "dev-owner"
	f.Organizations[0].ID = "00000000-0000-7000-8000-000000000002"
	for i := range f.Roles {
		f.Roles[i].Permissions = []fixturePermission{{Resource: "example", Action: "read"}}
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "probe.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_FIXTURE_PATH", path)
	if apply, err := PrepareInstallations(nil, "probe", false); err == nil || apply != nil || !strings.Contains(err.Error(), "local environment") {
		t.Fatalf("nonlocal installation fixture was not refused: %v", err)
	}
	if apply, err := PrepareInstallations(nil, "probe", true); err != nil || apply == nil {
		t.Fatalf("valid local fixture was not prepared: %v", err)
	}
}

type installationFixtureService struct {
	targets    []*business.AvailableSolutionTarget
	statuses   []gen.InstallationStatus
	installed  []*business.InstallSolutionParams
	grantCount int
	reads      int
	refusal    error
	retained   *gen.Installation
}

func TestFixtureInstallerMustMatchResolvedProviderIdentity(t *testing.T) {
	for _, id := range []string{"owner-id", "other-user-id", ""} {
		err := validateInstallationActors(context.Background(), exampleInstallationFixture(), func(_ context.Context, user fixtureUser) (*gen.User, error) {
			if user.Email != "owner@example.com" {
				t.Fatal("looked up wrong installer")
			}
			if id == "" {
				return nil, nil
			}
			return &gen.User{Uuid: id}, nil
		})
		if (err == nil) != (id == "owner-id") {
			t.Fatalf("identity %q: %v", id, err)
		}
	}
}

func (s *installationFixtureService) ListPrincipals(context.Context, string, string, int32, string) ([]*business.Principal, string, error) {
	return []*business.Principal{{ID: "principal-id", AgentIdentifier: "example/probe:1.0.0"}}, "", nil
}
func (s *installationFixtureService) ListInstallations(context.Context, *gen.ListInstallationsRequest) (*gen.ListInstallationsResponse, error) {
	response := &gen.ListInstallationsResponse{}
	if s.retained != nil {
		response.Installations = []*gen.InstallationSummary{{Installation: s.retained}}
	}
	return response, nil
}

func TestFixtureDoesNotRetargetOrReverseRevocation(t *testing.T) {
	for _, target := range []string{"replacement-target", "immutable-target"} {
		s := exampleInstallationService()
		s.retained = &gen.Installation{TargetId: target, AgentPrincipalId: "principal-id", Status: gen.InstallationStatus_INSTALLATION_STATUS_REVOKED}
		if target == "replacement-target" {
			s.retained.Status = gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE
		}
		if err := applyInstallations(context.Background(), s, exampleInstallationFixture(), time.Microsecond); err == nil {
			t.Fatal("retargeted or reversed revocation")
		}
		if len(s.installed) != 0 || s.grantCount != 0 {
			t.Fatal("mutated retained installation")
		}
	}
}

func (s *installationFixtureService) ListAvailableSolutions(context.Context, string, string, int) ([]*business.AvailableSolutionTarget, string, error) {
	return s.targets, "", nil
}
func (s *installationFixtureService) ListRoles(context.Context, *gen.ListRolesRequest) (*gen.ListRolesResponse, error) {
	return &gen.ListRolesResponse{Roles: []*gen.Role{{Id: "role-id", OrgId: "org-id", Name: "Example role"}, {Id: "use-id", OrgId: "org-id", Name: "Use example"}}}, nil
}
func (s *installationFixtureService) ListTeams(context.Context, *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error) {
	return &gen.ListTeamsResponse{Teams: []*gen.Team{{Id: "team-id", Name: "Example team"}}}, nil
}
func (s *installationFixtureService) InstallSolution(_ context.Context, actor string, params *business.InstallSolutionParams) (*gen.Installation, error) {
	if s.retained != nil && s.retained.Status == gen.InstallationStatus_INSTALLATION_STATUS_PENDING && s.reads < 2 {
		return nil, errors.New("existing pending installation requires its current lifecycle")
	}
	if actor != "owner-id" {
		return nil, errors.New("wrong fixture installer")
	}
	s.installed = append(s.installed, params)
	return &gen.Installation{Id: "installation-id"}, s.refusal
}

func TestFixtureRestartObservesPendingThenChecksConsentBeforeExposure(t *testing.T) {
	for _, changed := range []bool{false, true} {
		s := exampleInstallationService()
		s.retained = &gen.Installation{Id: "installation-id", TargetId: "immutable-target", AgentPrincipalId: "principal-id", Status: gen.InstallationStatus_INSTALLATION_STATUS_PENDING}
		if changed {
			s.refusal = errors.New("consent changed")
		}
		err := applyInstallations(context.Background(), s, exampleInstallationFixture(), time.Microsecond)
		if changed {
			if err == nil || s.grantCount != 0 {
				t.Fatal("exposed changed retained consent")
			}
		} else if err != nil || s.grantCount != 1 {
			t.Fatalf("pending restart did not finish: %v", err)
		}
		if len(s.installed) != 1 {
			t.Fatal("did not validate consent with canonical operation")
		}
	}
}
func (s *installationFixtureService) GetInstallation(context.Context, string, string) (*gen.Installation, gen.InstallationHealth, error) {
	index := s.reads
	if index >= len(s.statuses) {
		index = len(s.statuses) - 1
	}
	s.reads++
	return &gen.Installation{Id: "installation-id", Status: s.statuses[index]}, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, nil
}
func (s *installationFixtureService) GrantInstallationToTeam(_ context.Context, actor, org, installation, team, role string) (*business.InstallationGrant, error) {
	if s.reads == 0 || s.statuses[len(s.statuses)-1] != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
		return nil, errors.New("grant before acknowledgement")
	}
	if actor != "owner-id" || org != "org-id" || installation != "installation-id" || team != "team-id" || role != "use-id" {
		return nil, errors.New("wrong exposure")
	}
	s.grantCount++
	return &business.InstallationGrant{}, nil
}

func exampleInstallationFixture() *fixtureFile {
	return &fixtureFile{
		Users:         []fixtureUser{{ID: "owner-id", Email: "owner@example.com"}},
		Organizations: []fixtureOrg{{ID: "org-id", Name: "ExampleCorp", Owner: "owner@example.com"}},
		Teams:         []fixtureTeam{{Org: "ExampleCorp", Name: "Example team"}},
		Roles:         []fixtureRole{{Org: "ExampleCorp", Name: "Example role"}, {Org: "ExampleCorp", Name: "Use example"}},
		Installations: []fixtureInstallation{{Org: "ExampleCorp", Installer: "owner@example.com", Publisher: "example", Name: "probe", Version: "1.0.0", Role: "Example role", AllowedAudiences: []string{"example"}, AllowedScopes: []string{"example-records"}, Teams: []fixtureInstallationTeam{{Name: "Example team", Role: "Use example"}}}},
	}
}

func exampleInstallationService() *installationFixtureService {
	return &installationFixtureService{
		targets:  []*business.AvailableSolutionTarget{{TargetID: "immutable-target", ReleasePublisher: "example", ReleaseName: "probe", ReleaseVersion: "1.0.0"}},
		statuses: []gen.InstallationStatus{gen.InstallationStatus_INSTALLATION_STATUS_PENDING, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE},
	}
}

func TestFixtureInstallationUsesAcceptedIdentityAndWaitsForAcknowledgement(t *testing.T) {
	s := exampleInstallationService()
	if err := applyInstallations(context.Background(), s, exampleInstallationFixture(), time.Microsecond); err != nil {
		t.Fatal(err)
	}
	if len(s.installed) != 1 || s.installed[0].TargetID != "immutable-target" || s.installed[0].AgentIdentifier != "example/probe:1.0.0" || s.installed[0].RoleID != "role-id" || s.reads != 2 || s.grantCount != 1 {
		t.Fatalf("incorrect lifecycle: %+v", s)
	}
}

func TestFixtureInstallationRefusesAmbiguousOrDifferentReleases(t *testing.T) {
	for _, name := range []string{"publisher", "version", "ambiguous"} {
		t.Run(name, func(t *testing.T) {
			s := exampleInstallationService()
			switch name {
			case "publisher":
				s.targets[0].ReleasePublisher = "other"
			case "version":
				s.targets[0].ReleaseVersion = "2.0.0"
			case "ambiguous":
				s.targets = append(s.targets, s.targets[0])
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			if err := applyInstallations(ctx, s, exampleInstallationFixture(), time.Millisecond); err == nil {
				t.Fatal("accepted wrong or ambiguous release")
			}
			if len(s.installed) != 0 || s.grantCount != 0 {
				t.Fatal("mutated before resolving exact target")
			}
		})
	}
}

func TestFixtureInstallationNeverRetriesRefusalOrGrantsFailedInstallation(t *testing.T) {
	for _, name := range []string{"consent", "failed", "revoked", "deadline"} {
		t.Run(name, func(t *testing.T) {
			s := exampleInstallationService()
			switch name {
			case "consent":
				s.refusal = errors.New("existing consent differs")
			case "failed":
				s.statuses = []gen.InstallationStatus{gen.InstallationStatus_INSTALLATION_STATUS_FAILED}
			case "revoked":
				s.statuses = []gen.InstallationStatus{gen.InstallationStatus_INSTALLATION_STATUS_REVOKED}
			case "deadline":
				s.statuses = []gen.InstallationStatus{gen.InstallationStatus_INSTALLATION_STATUS_PENDING}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			if err := applyInstallations(ctx, s, exampleInstallationFixture(), time.Millisecond); err == nil {
				t.Fatal("ignored lifecycle failure")
			}
			if len(s.installed) != 1 || s.grantCount != 0 {
				t.Fatalf("retried or exposed refused installation: %+v", s)
			}
		})
	}
}

func TestInstallationFixtureValidatesReferencesBeforeSeeding(t *testing.T) {
	for _, name := range []string{"unpinned", "member", "role", "team", "duplicate", "version"} {
		t.Run(name, func(t *testing.T) {
			f := exampleInstallationFixture()
			switch name {
			case "unpinned":
				f.Users[0].ID = ""
			case "member":
				f.Organizations[0].Owner = "other@example.com"
			case "role":
				f.Installations[0].Role = "unknown"
			case "team":
				f.Installations[0].Teams[0].Name = "unknown"
			case "duplicate":
				f.Installations = append(f.Installations, f.Installations[0])
			case "version":
				f.Installations[0].Version = ""
			}
			if err := validateInstallations(f); err == nil {
				t.Fatal("invalid fixture accepted")
			}
		})
	}
	if err := validateInstallations(exampleInstallationFixture()); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureCancellationDoesNotSubmitInstallation(t *testing.T) {
	s := exampleInstallationService()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := applyInstallations(ctx, s, exampleInstallationFixture(), time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "canceled") || len(s.installed) != 0 {
		t.Fatalf("canceled fixture performed installation: %v", err)
	}
}

type pagedFixtureRegistry struct {
	installationFixtureService
	calls       int
	disappeared bool
}

func (s *pagedFixtureRegistry) ListAvailableSolutions(context.Context, string, string, int) ([]*business.AvailableSolutionTarget, string, error) {
	s.calls++
	switch s.calls {
	case 1:
		return s.targets, "later-page", nil
	case 2:
		return nil, "", business.ErrModuleRegistryStale
	default:
		if s.disappeared {
			return nil, "", nil
		}
		return s.targets, "", nil
	}
}
func TestFixtureDiscardsTargetFromAbortedScan(t *testing.T) {
	s := &pagedFixtureRegistry{installationFixtureService: *exampleInstallationService(), disappeared: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := applyInstallations(ctx, s, exampleInstallationFixture(), time.Microsecond)
	if !errors.Is(err, context.DeadlineExceeded) || len(s.installed) != 0 {
		t.Fatalf("installed a candidate from an aborted scan: %v", err)
	}
}
func TestFixtureStaleRegistryAfterMatchingPage(t *testing.T) {
	s := &pagedFixtureRegistry{installationFixtureService: *exampleInstallationService()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := applyInstallations(ctx, s, exampleInstallationFixture(), time.Microsecond); err != nil {
		t.Fatalf("a complete retry returned one exact accepted target, but setup failed: %v", err)
	}
}
