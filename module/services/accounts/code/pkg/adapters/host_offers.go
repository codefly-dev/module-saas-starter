package adapters

// OfferService, the setup read beside the installation interface: what a
// delivered solution's roles reach. Like the installation interface, every
// answer is the shared authority core's — Offers and ReviewOffer, read in one
// transaction from the solution's delivered declaration — so a solution's setup
// reads the same here and on the local authority. What this file adds is the
// wire: the core's offer projected into roles → operations → tools and the
// permission ceiling.
//
// Stopgap, labelled: the projection below is the local authority's
// (codefly-dev/authority modules/authority/services/local plugins/installation
// offers.go) ported line for line. It belongs in a shared authority library so
// both hosts call one function; until it moves there, the two copies must stay
// identical, and host_offers_test.go pins this one's behaviour.

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	corescope "github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	runnablev0 "github.com/codefly-dev/core/generated/go/codefly/runnable/v0"
	"github.com/codefly-dev/core/runnable"
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
	// runnables reads the composed tool bindings; nil when this host composes
	// none, and the review then says so (tools_composed false) rather than
	// listing no tool as if none existed.
	//
	// Production passes nil, and that is a known gap, not a choice: on a cell
	// the runnable-bindings group reaches only a service that declares it, and
	// declaring it also has the render grant that service an authority binding
	// per prepared key. The host is no tool caller and takes no such grant, so
	// until a service can receive the group without the grants, this host's
	// review lists no tool and says why.
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
	return reviewedOffer(core, ctx, offer, bindings)
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

// reviewedOffer is the offer as the interface states it: the consent identity,
// the installation when there is one, the ceiling of every binding the
// solution declares, and each role with the operations of every delivered
// module its scopes cover and the tools that expose them.
func reviewedOffer(core *authority.Service, ctx context.Context, offer authority.ReviewedOffer, bindings map[string]*runnablev0.PreparedBinding) (*installationv1.ReviewedOffer, error) {
	m, inst := offer.Module, offer.Installation
	a := offer.Approval
	out := &installationv1.ReviewedOffer{Target: m.Name, ToolsComposed: len(bindings) > 0}
	// A read the core answers without the consent tuple carries none: only an
	// administrator consents.
	if a.Digest != "" {
		out.Approval = &installationv1.ConsentApproval{TargetId: a.TargetID, Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, Digest: a.Digest}
	}
	if inst.ID != "" {
		out.Installation = installationToWire(core, ctx, inst)
	}
	for _, id := range slices.Sorted(maps.Keys(m.Bindings)) {
		b := m.Bindings[id]
		out.Ceiling = append(out.Ceiling, &installationv1.OfferBinding{Binding: id, Module: b.Audience, Scopes: offerScopes(b.InvokeScopes, false, nil)})
	}
	modules := slices.Clone(offer.Modules)
	slices.SortFunc(modules, func(a, b domain.Module) int { return strings.Compare(a.Name, b.Name) })
	for _, name := range slices.Sorted(maps.Keys(m.Roles)) {
		role := &installationv1.OfferRole{Name: name, Scopes: offerScopes(m.Roles[name], false, nil)}
		role.Scopes = append(role.Scopes, offerScopes(m.OwnedRoles[name], true, inst.Owned)...)
		// What the role reaches, by kind and action: its confinement to the
		// installation narrows resources, never which operations it covers.
		held := corescope.Unowned(stripIDs(append(slices.Clone(m.Roles[name]), m.OwnedRoles[name]...)))
		for _, target := range modules {
			for _, method := range slices.Sorted(maps.Keys(target.Methods)) {
				policy := target.Methods[method]
				if policy.AnyCheckedCaller || len(policy.Requires) == 0 || !corescope.Attenuates(policy.Requires, held) {
					continue
				}
				c := &installationv1.OfferCapability{Module: target.Name, Operation: method, Requires: offerScopes(policy.Requires, false, nil)}
				tools, err := toolsOf(target.Name, method, held, bindings)
				if err != nil {
					return nil, err
				}
				c.Tools = tools
				role.Capabilities = append(role.Capabilities, c)
			}
		}
		out.Roles = append(out.Roles, role)
	}
	return out, nil
}

// toolsOf is every tool-exposed Connect binding of one operation whose invoke
// and lookup scopes the role covers: the exposures a tool caller lists. The
// same tool composed from the operation's Connect and gRPC endpoints is one
// tool, named by its Connect binding, as a tool caller lists it.
func toolsOf(module, method string, held []*basev0.WorkScopeV1, bindings map[string]*runnablev0.PreparedBinding) ([]*installationv1.OfferTool, error) {
	var out []*installationv1.OfferTool
	keys := slices.Sorted(maps.Keys(bindings))
	connectFirst := func(key string) int {
		if bindings[key].GetOperation().GetEndpoint() == "connect" {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(keys, func(a, b string) int { return connectFirst(a) - connectFirst(b) })
	for _, key := range keys {
		b := bindings[key]
		op := b.GetOperation()
		if op.GetModule() != module || op.GetSpelling() != method || b.GetPolicy().GetTool() == nil || b.GetCall().GetConnect() == nil {
			continue
		}
		if !corescope.Attenuates(append(slices.Clone(b.GetPolicy().GetInvokeScopes()), b.GetPolicy().GetLookupScopes()...), held) {
			continue
		}
		tool, err := runnable.ToolFromPrepared(b)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "the composed binding %s is not a valid tool: %v", key, err)
		}
		if slices.ContainsFunc(out, func(t *installationv1.OfferTool) bool { return t.GetName() == tool.GetName() }) {
			continue
		}
		out = append(out, &installationv1.OfferTool{Name: tool.GetName(), Binding: key, Write: tool.GetEffect() != runnablev0.ToolExposure_EFFECT_READ_ONLY})
	}
	return out, nil
}

func stripIDs(in []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		out = append(out, &basev0.WorkScopeV1{ResourceKind: sc.GetResourceKind(), Actions: sc.GetActions()})
	}
	return out
}

func offerScopes(in []*basev0.WorkScopeV1, attached bool, owned map[string][]string) []*installationv1.OfferScope {
	var out []*installationv1.OfferScope
	for _, sc := range corescope.Normalize(in) {
		s := &installationv1.OfferScope{ResourceKind: sc.GetResourceKind(), Actions: sc.GetActions(), AttachedOnly: attached}
		if attached {
			s.ResourceIds = slices.Clone(owned[sc.GetResourceKind()])
		}
		out = append(out, s)
	}
	return out
}
