package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SourceGovernance ties a source to one installation of a role-gated target
// (a solution, or a module page declaring roles) and names the one scope that
// governs it: Kind and Action, one of the scopes the target's approved
// declaration names as governing its sources (core's module contract,
// source_governance). Binding is what an older request may still name; a
// declared governing scope is one of the target's own kinds, reached through
// nothing, so a request naming a binding is refused.
//
// The request never picks the governing scope: the host admits only one the
// target's author declared and an administrator approved, then asks the
// authority core whether it would issue the caller that scope at the
// installation, as it would for the target's page (IssueForPerson), so the
// answer is the core's role decision and never a second one. A target that
// declares no governing scope governs no source; a source without governance
// is the organization's own and is decided by the organization rule alone,
// as before. The tie is written once, at connect, and never moves.
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
	// The declaration the installation's consent approved, the one the core
	// issues from: a redelivery that widens a role grants nothing, here as in
	// issuance, until an administrator approves it.
	target, err := core.ApprovedDeclaration(ctx, inst)
	if err != nil {
		return err
	}
	if !decide.RoleGated(target) {
		return domain.Refuse("%s declares no roles, so it governs no source", inst.Target)
	}
	if err := governingScopeDeclared(target, g); err != nil {
		return err
	}
	asked := &basev0.WorkScopeV1{ResourceKind: g.Kind, Actions: []string{g.Action}}
	if target.Kind == domain.KindSolution {
		// A solution's roles grant only on the data its installation owns.
		asked.ResourceOwnerId = inst.PrincipalID
	}
	_, err = core.IssueForPerson(ctx, who, authority.IssueRequest{
		Audience: target.Principal, Organization: who.Organization,
		Scopes: decide.Scopes{asked}, TTL: sourceGovernanceProbeTTL,
	})
	return err
}

// governingScopeDeclared admits g only when target's approved declaration
// names its scope as governing its sources: the author's call, which an
// administrator approved with the rest of the offer. A target naming none
// governs no source, so its sources stay the organization administrators'.
// A declared scope is one of the target's own kinds, so a request naming a
// binding names something no declaration governs by.
func governingScopeDeclared(target domain.Module, g SourceGovernance) error {
	if len(target.SourceGovernance) == 0 {
		return domain.Refuse("%s declares no scope that governs its sources, so they stay the organization administrators'", target.Name)
	}
	if g.Binding != "" {
		return domain.Refuse("%s governs its sources by scopes of its own kinds, never through binding %s", target.Name, g.Binding)
	}
	if !decide.Governs(target, g.Kind, g.Action) {
		declared := make([]string, 0, len(target.SourceGovernance))
		for _, sc := range target.SourceGovernance {
			for _, action := range sc.GetActions() {
				declared = append(declared, sc.GetResourceKind()+":"+action)
			}
		}
		return domain.Refuse("%s does not declare %s:%s as governing its sources; it declares %s", target.Name, g.Kind, g.Action, strings.Join(declared, ", "))
	}
	return nil
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

// AuthorizeSourceChange decides a change to a governed source by who, a member
// of its organization: the core must issue who its governing scope at its
// installation, and anyone it does not, an organization administrator
// included, is refused by name. It answers nothing for a source without a
// live governance (one never tied, or whose installation was uninstalled):
// that is the organization's own, its administrators' to change, which the
// caller decides (SourceGoverned).
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

// resolveSourceBoundary is resolveBoundary for a connect: an organization's
// own source may name any node of the organization's, or a label reusing any
// collection of that name, as before; a governed source writes only into its
// installation's own collections — a node a source the same installation
// governs already writes into, or a label reusing one of those or minting a
// new one — never another node of the organization's, which its connecting
// member may administer nothing of. Runs inside the source-insert transaction.
func (s *Service) resolveSourceBoundary(ctx context.Context, orgID string, g *SourceGovernance, boundaryNodeID, collectionLabel string) (string, error) {
	if g == nil {
		return s.resolveBoundary(ctx, orgID, boundaryNodeID, collectionLabel)
	}
	if boundaryNodeID = strings.TrimSpace(boundaryNodeID); boundaryNodeID != "" {
		own, err := s.store.GovernedBoundaryNode(ctx, orgID, g.InstallationID, boundaryNodeID)
		if err != nil {
			return "", err
		}
		if !own {
			return "", status.Errorf(codes.PermissionDenied, "boundary node %s is not a collection of installation %s: a source it governs writes only into its own collections", boundaryNodeID, g.InstallationID)
		}
		return boundaryNodeID, nil
	}
	node := &gen.ScopeNode{Id: NewIDString(), OrgId: orgID, Kind: ScopeNodeKindCollection, Label: strings.TrimSpace(collectionLabel)}
	node.ScopePath = strings.ReplaceAll(node.Id, "-", "_")
	return s.store.GetOrCreateGovernedCollectionNode(ctx, node, g.InstallationID)
}

// SourceGoverned reports whether source is governed now: it carries a
// governance whose installation was not uninstalled.
func (s *Service) SourceGoverned(ctx context.Context, source *DatasourceSource) (bool, error) {
	if source == nil || source.Governance == nil {
		return false, nil
	}
	_, live, err := s.liveGovernance(ctx, source)
	return live, err
}
