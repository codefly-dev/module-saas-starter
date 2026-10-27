package adapters

// OfferService, the setup read beside the installation interface: what a
// delivered solution's roles reach. Like the installation interface, every
// answer is the shared authority core's — Offers and ReviewOffer, read in one
// transaction from the solution's delivered declaration — so a solution's setup
// reads the same here and on the local authority. What this file adds is the
// wire: the core's offer projected into roles → operations → tools and the
// permission ceiling.
//
// The authority-owned offers.Project supplies the projection. This adapter
// only converts that result into this host's generated messages.

import (
	"context"
	"slices"
	"strings"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/offers"
	runnablev0 "github.com/codefly-dev/core/generated/go/codefly/runnable/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	installationv1 "accounts/pkg/gen/host/installation/v1"
)

// RunnableReader reads the prepared Runnable bindings (keyed MODULE__OPERATION)
// a tool caller of this host runs, so a review can name each operation's tools.
type RunnableReader func(context.Context) (map[string]*runnablev0.PreparedBinding, error)

// HostOfferServer serves OfferService.
type HostOfferServer struct {
	installationv1.UnimplementedOfferServiceServer
	// runnables supplies composed tool metadata. Production has no reader yet,
	// so exposure is reported as unavailable, not absent. The CLI treats a
	// runnable-bindings consumer as an executor (including network reach);
	// a read-only delivery must be qualified before enabling this reader.
	runnables RunnableReader
}

var hostOfferSingleton = &HostOfferServer{}

// HostOfferSingleton serves OfferService.
func HostOfferSingleton() *HostOfferServer { return hostOfferSingleton }

// ListOffers lists the delivered solutions, for an administrator.
func (s *HostOfferServer) ListOffers(ctx context.Context, req *installationv1.ListOffersRequest) (*installationv1.ListOffersResponse, error) {
	core, caller, err := offerCaller(ctx, "list the solutions on offer", req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	offers, err := core.Offers(ctx, caller, req.GetOrganizationId())
	if err != nil {
		return nil, coreStatus(err)
	}
	slices.SortFunc(offers, func(a, b domain.Module) int { return strings.Compare(a.Name, b.Name) })
	out := &installationv1.ListOffersResponse{}
	for _, m := range offers {
		out.Offers = append(out.Offers, &installationv1.Offer{Target: m.Name, Generation: m.Generation})
	}
	return out, nil
}

// ReviewOffer projects one consistent read of the offer, for whoever the core
// answers: an administrator, with the consent Install takes, or whoever else
// the core lets read it, without it. No floor is added here, so the answer and
// every refusal are the core's own. The tools are joined from the composed
// bindings by operation; nothing here is a decision.
func (s *HostOfferServer) ReviewOffer(ctx context.Context, req *installationv1.ReviewRequest) (*installationv1.ReviewedOffer, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, "review an offer")
	if err != nil {
		return nil, err
	}
	offer, err := core.ReviewOffer(ctx, caller, req.GetOrganizationId(), req.GetTarget())
	if err != nil {
		return nil, coreStatus(err)
	}
	var bindings map[string]*runnablev0.PreparedBinding
	if s.runnables != nil {
		if bindings, err = s.runnables(ctx); err != nil {
			return nil, status.Errorf(codes.Unavailable, "the composed tool bindings cannot be read: %v", err)
		}
	}
	return reviewedOffer(offer, bindings)
}

// offerCaller is the signed-in administrator of org, as listing the offers
// requires: the policy's floor, then the core's own decision on the same person.
func offerCaller(ctx context.Context, what, org string) (*authority.Service, authority.Person, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, authority.Person{}, err
	}
	caller, err := signedInPerson(ctx, what)
	if err != nil {
		return nil, authority.Person{}, err
	}
	if err := requireOrgAdmin(ctx, caller.ID, org); err != nil {
		return nil, authority.Person{}, err
	}
	return core, caller, nil
}

// reviewedOffer adapts the authority-owned projection to this host's wire.
func reviewedOffer(offer authority.ReviewedOffer, bindings map[string]*runnablev0.PreparedBinding) (*installationv1.ReviewedOffer, error) {
	projected, err := offers.Project(offer, bindings)
	if err != nil {
		return nil, coreStatus(err)
	}
	out := &installationv1.ReviewedOffer{Target: projected.Target, ToolsComposed: projected.ToolsComposed}
	if a := projected.Approval; a != nil {
		out.Approval = &installationv1.ConsentApproval{TargetId: a.TargetID, Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, Digest: a.Digest}
	}
	if projected.Installation != nil {
		roles := make([]string, 0, len(projected.Roles))
		for _, role := range projected.Roles {
			roles = append(roles, role.Name)
		}
		out.Installation = installationToWireWithRoles(*projected.Installation, roles)
	}
	for _, b := range projected.Ceiling {
		out.Ceiling = append(out.Ceiling, &installationv1.OfferBinding{Binding: b.Binding, Module: b.Module, Scopes: offerScopesToWire(b.Scopes)})
	}
	for _, r := range projected.Roles {
		role := &installationv1.OfferRole{Name: r.Name, Scopes: offerScopesToWire(r.Scopes)}
		for _, c := range r.Capabilities {
			capability := &installationv1.OfferCapability{Module: c.Module, Operation: c.Operation, Requires: offerScopesToWire(c.Requires)}
			for _, t := range c.Tools {
				capability.Tools = append(capability.Tools, &installationv1.OfferTool{Name: t.Name, Binding: t.Binding, Write: t.Write})
			}
			role.Capabilities = append(role.Capabilities, capability)
		}
		out.Roles = append(out.Roles, role)
	}
	return out, nil
}

func offerScopesToWire(scopes []*offers.Scope) []*installationv1.OfferScope {
	var out []*installationv1.OfferScope
	for _, s := range scopes {
		out = append(out, &installationv1.OfferScope{ResourceKind: s.ResourceKind, Actions: s.Actions, AttachedOnly: s.AttachedOnly, ResourceIds: s.ResourceIDs})
	}
	return out
}
