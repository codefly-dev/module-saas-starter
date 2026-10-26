package adapters

// A workload's own issue behind the host interface: the module proves which
// module it is by its credential alone, and is issued either the host's
// operation context through a binding it declares (the headless mint) or its
// own context to this surface. The ask is honoured below what the workload is
// entitled to and never above it: scopes, operation and lifetime narrow the
// binding's grant, an audience other than the binding's is refused by name,
// and anything outside the grant is refused rather than issued.

import (
	"context"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// workloadIssue authenticates the workload, then issues through the binding
// req names, or the module's own context when it names none.
func (s *HostAuthorityServer) workloadIssue(ctx context.Context, credential string, req *authorityv1.IssueRequest, ttl int32) (*gen.IssuedWorkContext, error) {
	prefix, err := service.ModulePrefixOfWorkload(ctx, credential)
	if err != nil {
		return nil, credentialRefusal(err, "workload")
	}
	binding := req.GetOperationBindingId()
	if binding == "" {
		return s.workloadOwnContext(ctx, prefix, credential, req, ttl)
	}
	if err := requireComposed(req.GetAudience()); err != nil {
		return nil, err
	}
	declared, ok := service.ModulePrincipals()[business.ModulePrincipalID(prefix)].OperationAudiences[binding]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation binding %q is not declared by %s", binding, prefix)
	}
	if declared.Audience != req.GetAudience() {
		return nil, status.Errorf(codes.PermissionDenied, "operation binding %q is addressed to %s, not %s", binding, declared.Audience, req.GetAudience())
	}
	lookup := false
	switch operation := req.GetOperation(); operation {
	case "", "headless":
	case "lookup":
		if len(declared.LookupScopes) == 0 {
			return nil, status.Errorf(codes.PermissionDenied, "operation binding %q declares no lookup", binding)
		}
		lookup = true
	case "invoke":
		return nil, status.Errorf(codes.InvalidArgument, "a workload acts as itself: invoke is a person's operation on binding %q; a workload's are headless and lookup", binding)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "operation %q is none of invoke, headless and lookup", operation)
	}
	narrow := func(authority business.ModuleOperationContextAuthority) (business.ModuleOperationContextAuthority, error) {
		return narrowOperationContext(authority, declared, lookup, req.GetScopes(), workContextTTL(ttl))
	}
	out, err := askingModuleCapabilities(s.modules, moduleMintAsk{narrow: narrow}).MintModuleOperationContext(ctx, &gen.ModuleMintOperationContextRequest{
		Prefix: prefix, ServiceAccountToken: credential, Binding: binding, OrganizationId: req.GetOrganizationId(),
	})
	if err != nil {
		return nil, err
	}
	return &gen.IssuedWorkContext{Token: out.GetToken(), ExpiresAt: out.GetExpiresAt()}, nil
}

// workloadOwnContext is the module's own context to this surface: its identity
// here, which carries no scopes and names no operation (what it may do is
// re-read on every call), so an ask for either is refused by name.
func (s *HostAuthorityServer) workloadOwnContext(ctx context.Context, prefix, credential string, req *authorityv1.IssueRequest, ttl int32) (*gen.IssuedWorkContext, error) {
	if req.GetAudience() != ModuleWorkContextAudience {
		return nil, status.Errorf(codes.InvalidArgument, "a module's own context is addressed to %q, not %q", ModuleWorkContextAudience, req.GetAudience())
	}
	if len(req.GetScopes()) > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s's own context carries no scopes: it is the module's identity to this host, and what it may do is re-read on every call", prefix)
	}
	if operation := req.GetOperation(); operation != "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s's own context names no operation; %q is one of a binding's", prefix, operation)
	}
	out, err := askingModuleCapabilities(s.modules, moduleMintAsk{ttl: workContextTTL(ttl)}).MintModuleWorkContext(ctx, &gen.ModuleMintWorkContextRequest{
		Prefix: prefix, ServiceAccountToken: credential, OrganizationId: req.GetOrganizationId(),
	})
	if err != nil {
		return nil, err
	}
	return &gen.IssuedWorkContext{Token: out.GetToken(), ExpiresAt: out.GetExpiresAt()}, nil
}

// narrowOperationContext holds a resolved headless operation context to the
// ask. The ceiling is the installed binding's headless grant, or for a lookup
// the binding's lookup scopes within it; an empty ask is the whole ceiling, an
// ask outside it is refused naming the scope, and a lifetime only shortens.
func narrowOperationContext(
	authority business.ModuleOperationContextAuthority, declared business.ModuleOperationAudience,
	lookup bool, asked []*authorityv1.WorkScopeV1, ttl time.Duration,
) (business.ModuleOperationContextAuthority, error) {
	reads := mintContentReads(authority.Audience)
	_, ceiling, err := workContextScopes(authority.WireScopes(), reads)
	if err != nil {
		return authority, status.Error(codes.Internal, err.Error())
	}
	operation := "headless"
	if lookup {
		operation = "lookup"
		_, lookups, err := workContextScopes(declared.WireScopes(true), reads)
		if err != nil {
			return authority, status.Error(codes.Internal, err.Error())
		}
		ceiling = withinScopes(lookups, ceiling)
		if len(ceiling) == 0 {
			return authority, status.Errorf(codes.PermissionDenied, "operation binding %q's lookup lies outside what it grants headless", authority.BindingID)
		}
	}
	scopes := ceiling
	if len(asked) > 0 {
		_, wanted, err := workContextScopes(scopesToHost(asked), reads)
		if err != nil {
			return authority, status.Error(codes.InvalidArgument, err.Error())
		}
		for _, scope := range wanted {
			if !workcontext.ScopeContained(scope, ceiling) {
				return authority, status.Errorf(codes.PermissionDenied, "a workload's issue widens what operation binding %q grants it for %s: %s", authority.BindingID, operation, scopeString(scope))
			}
		}
		scopes = wanted
	}
	authority.Scopes = operationScopesFromCore(scopes)
	authority.TTL = ttl
	return authority, nil
}

// withinScopes is each scope of want held to the first scope of its kind in
// bound, as core reads a set; a kind bound does not carry, or one left with
// nothing in common, is dropped.
func withinScopes(want, bound []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, scope := range want {
		for _, limit := range bound {
			if limit.GetResourceKind() != scope.GetResourceKind() {
				continue
			}
			if kept := intersectScope(limit, scope); kept != nil {
				out = append(out, kept)
			}
			break
		}
	}
	return out
}
