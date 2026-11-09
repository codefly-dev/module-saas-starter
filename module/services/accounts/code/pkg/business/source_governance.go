package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SourceGovernance ties a source to one installation of a role-gated target
// (a solution, or a module page declaring roles) and names the one scope of
// that target's own declaration that governs it: Kind and Action, held on the
// target's own kinds or, with Binding, reached through that operation binding.
//
// The target declares the scope, the host declares nothing: the host only asks
// the authority core whether it would issue the caller that scope at the
// installation, as it would for the target's page (IssueForPerson), so the
// answer is the core's role decision and never a second one. A source without
// governance is the organization's own and is decided by the organization rule
// alone, as before. The tie is written once, at connect, and never moves.
type SourceGovernance struct {
	InstallationID string
	Kind, Action   string
	Binding        string
}

// sourceGovernanceProbeTTL bounds the capability the governance check has the
// core sign and then discards: only the decision is used.
const sourceGovernanceProbeTTL = time.Minute

func (g SourceGovernance) scope() string {
	if g.Binding == "" {
		return g.Kind + ":" + g.Action
	}
	return g.Kind + ":" + g.Action + " through binding " + g.Binding
}

// validate refuses a governance that names no installation or no scope.
func (g *SourceGovernance) validate() error {
	if g == nil {
		return nil
	}
	g.InstallationID, g.Kind, g.Action, g.Binding = strings.TrimSpace(g.InstallationID), strings.TrimSpace(g.Kind), strings.TrimSpace(g.Action), strings.TrimSpace(g.Binding)
	if g.InstallationID == "" || g.Kind == "" || g.Action == "" {
		return status.Error(codes.InvalidArgument, "a source's governance names its installation, and the resource kind and action that govern it")
	}
	return nil
}

// governingInstallation reads the installation g names at org from the core's
// records, as the host reads them. found is false for none at org.
func (s *Service) governingInstallation(ctx context.Context, org, id string) (inst domain.Installation, found bool, err error) {
	cores, ok := s.store.(AuthorityCoreStore)
	if !ok {
		return inst, false, status.Error(codes.Unavailable, ErrAuthorityCoreUnconfigured.Error())
	}
	err = cores.AuthorityCoreWithConsent(nil).Read(ctx, func(tx authority.Tx) error {
		read, err := tx.Installation(id)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		inst, found = read, read.Organization == org
		return nil
	})
	return inst, found, err
}

// issuedGovernance is the core's answer to whether it issues who g's scope at
// the installation inst: nil when it does, the core's refusal otherwise.
func (s *Service) issuedGovernance(ctx context.Context, who authority.Person, inst domain.Installation, g SourceGovernance) error {
	core, err := s.AuthorityCore()
	if err != nil {
		return status.Errorf(codes.Unavailable, "the source's governance at installation %s cannot be decided: %v", inst.ID, err)
	}
	target, err := core.Composed(ctx, inst.Target)
	if err != nil {
		return err
	}
	if !decide.RoleGated(target) {
		return domain.Refuse("%s declares no roles, so it governs no source", inst.Target)
	}
	asked := &basev0.WorkScopeV1{ResourceKind: g.Kind, Actions: []string{g.Action}}
	if target.Kind == domain.KindSolution {
		// A solution's roles grant only on the data its installation owns.
		asked.ResourceOwnerId = inst.PrincipalID
	}
	_, err = core.IssueForPerson(ctx, who, authority.IssueRequest{
		Audience: target.Principal, Binding: g.Binding, Organization: who.Organization,
		Scopes: decide.Scopes{asked}, TTL: sourceGovernanceProbeTTL,
	})
	return err
}

// governanceRefusal names who was refused what, and why, on one source.
func governanceRefusal(err error, format string, args ...any) error {
	code := codes.PermissionDenied
	if c, ok := domain.CodeOf(err); ok && c == domain.Precondition {
		code = codes.FailedPrecondition
	} else if s, ok := status.FromError(err); ok && s.Code() == codes.Unavailable {
		code = codes.Unavailable
	}
	reason := err.Error()
	if s, ok := status.FromError(err); ok {
		reason = s.Message()
	}
	return status.Errorf(code, "%s: %s", fmt.Sprintf(format, args...), reason)
}

// AuthorizeSourceGovernance decides a connect that ties a new source to g's
// installation: the installation is an active one of the caller's
// organization, and the core issues the caller g's scope there now. It is the
// governance check a later change repeats (AuthorizeSourceChange), asked
// before anything is stored.
func (s *Service) AuthorizeSourceGovernance(ctx context.Context, who authority.Person, g *SourceGovernance) error {
	if g == nil {
		return nil
	}
	if err := g.validate(); err != nil {
		return err
	}
	inst, found, err := s.governingInstallation(ctx, who.Organization, g.InstallationID)
	if err != nil {
		return err
	}
	if !found || inst.Status != domain.Active {
		return status.Errorf(codes.FailedPrecondition, "installation %s is not an active installation of organization %s, so it governs no new source", g.InstallationID, who.Organization)
	}
	if err := s.issuedGovernance(ctx, who, inst, *g); err != nil {
		return governanceRefusal(err, "%s may not connect a source governed by %s at installation %s of %s", who.ID, g.scope(), inst.ID, inst.Target)
	}
	return nil
}

// AuthorizeSourceChange decides a change to source by who, beyond the
// organization rule its method declares: an organization's own source needs
// nothing more; a governed one needs the core to issue who its governing scope
// at its installation, and an organization administrator who is not issued it
// is refused by name. A governance whose installation was uninstalled has
// lapsed: the source is the organization's again, so its administrators can
// still remove what no role governs any more.
func (s *Service) AuthorizeSourceChange(ctx context.Context, who authority.Person, source *DatasourceSource) error {
	g := source.Governance
	if g == nil {
		return nil
	}
	inst, live, err := s.liveGovernance(ctx, source)
	if err != nil || !live {
		return err
	}
	if err := s.issuedGovernance(ctx, who, inst, *g); err != nil {
		return governanceRefusal(err, "source %s belongs to installation %s of %s, and changing it requires %s there, which %s is not issued", source.ID, inst.ID, inst.Target, g.scope(), who.ID)
	}
	return nil
}

// AuthorizeSourceChangeByID is AuthorizeSourceChange for the source id of org.
// An absent source is left to the change itself, which answers for it as it
// always has.
func (s *Service) AuthorizeSourceChangeByID(ctx context.Context, who authority.Person, org, id string) error {
	source, err := s.GetDatasourceSource(ctx, org, id)
	if errors.Is(err, ErrDatasourceSourceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.AuthorizeSourceChange(ctx, who, source)
}

// AuthorizeSourceRead decides whether who may read source: an organization's
// own source, or one whose governance lapsed, is read under the organization
// rule alone; a governed one only by someone holding a role on its
// installation, the roles the installation's own readers hold.
func (s *Service) AuthorizeSourceRead(ctx context.Context, who authority.Person, source *DatasourceSource) error {
	readable, err := s.ReadableSources(ctx, who, []*DatasourceSource{source})
	if err != nil {
		return err
	}
	if len(readable) == 0 {
		return status.Errorf(codes.PermissionDenied, "source %s belongs to installation %s, and reading it requires a role there, which %s does not hold", source.ID, source.Governance.InstallationID, who.ID)
	}
	return nil
}

// ReadableSources are the sources of sources who may read (AuthorizeSourceRead),
// in order. who's held installations are read once, from the core.
func (s *Service) ReadableSources(ctx context.Context, who authority.Person, sources []*DatasourceSource) ([]*DatasourceSource, error) {
	var held map[string]bool
	out := make([]*DatasourceSource, 0, len(sources))
	for _, source := range sources {
		if source.Governance == nil {
			out = append(out, source)
			continue
		}
		_, live, err := s.liveGovernance(ctx, source)
		if err != nil {
			return nil, err
		}
		if !live {
			out = append(out, source)
			continue
		}
		if held == nil {
			if held, err = s.heldInstallations(ctx, who); err != nil {
				return nil, err
			}
		}
		if held[source.Governance.InstallationID] {
			out = append(out, source)
		}
	}
	return out, nil
}

// heldInstallations are the installations who holds a role on, as the core
// answers a member's navigation. A caller the core does not read as a member
// signed in at the organization holds none.
func (s *Service) heldInstallations(ctx context.Context, who authority.Person) (map[string]bool, error) {
	held := map[string]bool{}
	if who.ID == "" {
		return held, nil
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "governed sources cannot be read: %v", err)
	}
	installations, err := core.HeldInstallations(ctx, who, who.Organization)
	if code, ok := domain.CodeOf(err); ok && code == domain.Denied {
		return held, nil
	}
	if err != nil {
		return nil, err
	}
	for _, h := range installations {
		held[h.Installation.ID] = true
	}
	return held, nil
}

// liveGovernance is source's governing installation and whether it still
// governs: false once it was uninstalled (or is gone), when the source is the
// organization's own again.
func (s *Service) liveGovernance(ctx context.Context, source *DatasourceSource) (domain.Installation, bool, error) {
	inst, found, err := s.governingInstallation(ctx, source.OrgID, source.Governance.InstallationID)
	if err != nil {
		return inst, false, err
	}
	return inst, found && inst.Status != domain.Revoked, nil
}
