package adapters

import (
	"context"
	"slices"
	"sort"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The lasting limits a chain of capabilities puts on each resource kind it has
// visited (rule A: a narrowing is never recovered).
//
// A kind's limit is, per action, the resources the chain still allows: every
// resource, or the named ones. A kind the chain dropped is limited to no
// action at all, and is carried as a scope naming the kind and no action, an
// explicit denial a later hop reads back. A kind the chain never named is not
// limited: that is how a chain crosses into an audience whose kinds it never
// carried.

// kindLimit is, per action, the resources allowed: nil is every resource.
type kindLimit map[string][]string

// narrowings is each visited kind's limit.
type narrowings map[string]kindLimit

// limitOf is the coverage of one scope set on kind: each row's actions on its
// resources, a kind-wide row covering every resource of its actions.
func limitOf(set []*basev0.WorkScopeV1, kind string) kindLimit {
	out := kindLimit{}
	for _, scope := range set {
		if scope.GetResourceKind() != kind {
			continue
		}
		for _, action := range scope.GetActions() {
			ids, seen := out[action]
			switch {
			case seen && ids == nil:
			case len(scope.GetResourceIds()) == 0:
				out[action] = nil
			default:
				out[action] = union(ids, scope.GetResourceIds())
			}
		}
	}
	return out
}

// parentNarrowings is the limit a parent's every scope set puts on each kind
// any of them names: its authority scopes and each hop's grant, met.
func parentNarrowings(parent *basev0.WorkContextV1) narrowings {
	sets := [][]*basev0.WorkScopeV1{parent.GetAuthorityScopes()}
	for _, hop := range parent.GetActorChain() {
		sets = append(sets, hop.GetGrantedScopes())
	}
	out := narrowings{}
	for _, set := range sets {
		kinds := map[string]bool{}
		for _, scope := range set {
			kinds[scope.GetResourceKind()] = true
		}
		for kind := range kinds {
			limit := limitOf(set, kind)
			if previous, ok := out[kind]; ok {
				limit = previous.meet(limit)
			}
			out[kind] = limit
		}
	}
	return out
}

// meet is what both limits allow.
func (l kindLimit) meet(other kindLimit) kindLimit {
	out := kindLimit{}
	for action, ids := range l {
		theirs, ok := other[action]
		if !ok {
			continue
		}
		if met, any := meetIDs(ids, theirs); any {
			out[action] = met
		}
	}
	return out
}

// meetIDs is the resources both allow, nil meaning every resource; any is
// false when they allow none in common.
func meetIDs(a, b []string) ([]string, bool) {
	switch {
	case a == nil:
		return slices.Clone(b), true
	case b == nil:
		return slices.Clone(a), true
	}
	var out []string
	for _, id := range a {
		if slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	return out, len(out) > 0
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, id := range b {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// keep is scopes with each limited kind held to its limit; what a limit allows
// none of is dropped.
func (n narrowings) keep(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, scope := range scopes {
		limit, limited := n[scope.GetResourceKind()]
		if !limited {
			out = append(out, scope)
			continue
		}
		within := kindLimit{}
		for _, action := range scope.GetActions() {
			allowed, ok := limit[action]
			if !ok {
				continue
			}
			var ids []string
			if len(scope.GetResourceIds()) > 0 {
				ids = scope.GetResourceIds()
			}
			if met, any := meetIDs(ids, allowed); any {
				within[action] = met
			}
		}
		out = append(out, within.scopes(scope.GetResourceKind())...)
	}
	return out
}

// deny is n with every kind of domain that granted does not name limited to
// nothing: a kind the owner could have carried here and the grant left out.
func (n narrowings) deny(domain, granted []*basev0.WorkScopeV1) narrowings {
	out := narrowings{}
	for kind, limit := range n {
		out[kind] = limit
	}
	for _, scope := range domain {
		kind := scope.GetResourceKind()
		if !slices.ContainsFunc(granted, func(g *basev0.WorkScopeV1) bool { return g.GetResourceKind() == kind }) {
			out[kind] = kindLimit{}
		}
	}
	return out
}

// outside is the limit of every kind scopes does not name, as scopes: a kind
// limited to nothing as its denial, a scope naming the kind and no action.
func (n narrowings) outside(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	named := map[string]bool{}
	for _, scope := range scopes {
		named[scope.GetResourceKind()] = true
	}
	kinds := make([]string, 0, len(n))
	for kind := range n {
		if !named[kind] {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds)
	var out []*basev0.WorkScopeV1
	for _, kind := range kinds {
		if rows := n[kind].scopes(kind); len(rows) > 0 {
			out = append(out, rows...)
		} else {
			out = append(out, &basev0.WorkScopeV1{ResourceKind: kind})
		}
	}
	return out
}

// scopes is a limit as rows of kind: actions allowed on the same resources
// share one row, in a stable order.
func (l kindLimit) scopes(kind string) []*basev0.WorkScopeV1 {
	actions := make([]string, 0, len(l))
	for action := range l {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	var out []*basev0.WorkScopeV1
	for _, action := range actions {
		ids := l[action]
		i := slices.IndexFunc(out, func(row *basev0.WorkScopeV1) bool {
			return (row.ResourceIds == nil) == (ids == nil) && slices.Equal(row.ResourceIds, ids)
		})
		if i >= 0 {
			out[i].Actions = append(out[i].Actions, action)
			continue
		}
		out = append(out, &basev0.WorkScopeV1{ResourceKind: kind, Actions: []string{action}, ResourceIds: slices.Clone(ids)})
	}
	return out
}

// droppedKindsKey carries, from the interface's person issue to the mint, the
// kinds the capability drops. It is set only inside this process, never read
// from a request.
type droppedKindsKey struct{}

func withDroppedKinds(ctx context.Context, kinds []string) context.Context {
	if len(kinds) == 0 {
		return ctx
	}
	return context.WithValue(ctx, droppedKindsKey{}, kinds)
}

// droppedAudienceKinds is every kind the audience declares that scopes do not
// name.
func droppedAudienceKinds(audience string, scopes []*gen.WorkContextScope) []string {
	if service == nil {
		return nil
	}
	kinds, err := service.ModuleContentResources(audience)
	if err != nil {
		return nil
	}
	var out []string
	for _, kind := range kinds {
		if !slices.ContainsFunc(scopes, func(s *gen.WorkContextScope) bool { return s.GetResourceKind() == kind }) {
			out = append(out, kind)
		}
	}
	return out
}

// droppedKindScopes is the denial of each kind the context drops that scopes
// do not name: a scope naming the kind and no action.
func droppedKindScopes(ctx context.Context, scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	kinds, _ := ctx.Value(droppedKindsKey{}).([]string)
	var out []*basev0.WorkScopeV1
	for _, kind := range kinds {
		if !slices.ContainsFunc(scopes, func(s *basev0.WorkScopeV1) bool { return s.GetResourceKind() == kind }) {
			out = append(out, &basev0.WorkScopeV1{ResourceKind: kind})
		}
	}
	return out
}
