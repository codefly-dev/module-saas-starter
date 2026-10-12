package main

// Admitting a RESOURCE-CONFINED credential at a declared module route
// (register finding SA-F-MCPAUD, round 3).
//
// A credential delivered to one consumer is confined to it: ext_authz admits a
// resource-bound token at the solution its resource names, at the delegation
// mint, and nowhere else. That left the consumer unable to do the one thing it
// was delivered for. The flow is three requests, not one:
//
//  1. a client authorizes for a solution's tool endpoint and calls it;
//  2. the solution's runtime mints a Work Context for the composed module it
//     declared it consumes, forwarding the viewer's bearer, so the capability is
//     owned and actored by the person rather than by the runtime;
//  3. the runtime calls `/v1/<alias>/...` with that capability AND the same
//     bearer, because a declared upstream authenticates the request by the
//     bearer (SA-F-BEARER, open).
//
// Step 3 was refused, so the tool failed one request later than before. The
// naive repair — admit the bearer at module routes — would have handed a
// one-solution credential the person's whole authority at every declared module,
// which is the confinement deleted rather than relocated.
//
// THE RULE: a resource-confined bearer is admitted at a declared module route
// only TOGETHER WITH an inspected capability for that module, in the bearer's
// own organization, minted in the bearer's own SESSION. The capability is what
// carries the ceiling — accounts holds a confined mint to the installed
// consumer's declared audiences and its installation's standing grants (see
// confineResourceMintToItsConsumer and accounts' StartTask) — and the bindings
// below are what stop a capability minted by anybody else standing in for it.
//
// The session binding is the one that makes "minted under that ceiling" a fact
// rather than an inference, and it is sufficient on its own to name the owner:
// accounts signs into a capability only its caller's VERIFIED session or a
// fresh one, never a session a request names (callerSession in accounts), and a
// session belongs to one person. So a capability carrying the bearer's session
// was minted by this very credential — never by the same person's unconfined
// browser session, whose session id is its own, and never by anyone else. The
// owner is therefore not compared here, which keeps this hop to what core's
// Inspector exposes: a hop routes, it never reads the principal it would act as.

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/workcontext"
)

// moduleCapabilityPairingKey carries the verified capability from the declared
// module route to ext_authz.
//
// It travels in the request CONTEXT and not in a header, deliberately. A header
// would be a value this process asserts to itself through a channel a caller can
// also write to; it would have to be stripped, listed, and trusted on the
// strength of that stripping. The context value is set by this process, after
// the signature check, on the one route that resolves to a declared module, and
// is unreachable from the request.
type moduleCapabilityPairingKey struct{}

// moduleCapabilityPairing is the inspected capability a declared module route
// presented, reduced to the facts a confined credential is paired against.
type moduleCapabilityPairing struct {
	// audience is the capability's own audience, as signed.
	audience string
	// viewerAudience is the audience a VIEWER's delegated capability for this
	// route carries — the module's own, which accounts derives from the declared
	// module principal and the runtime names as its consumption target.
	viewerAudience string
	tenant         string
	session        string
}

// withPresentedModuleCapability records a verified capability for the route
// about to be judged. A nil claims set records nothing, so "no capability was
// presented" and "one was presented and verified" stay distinguishable.
func withPresentedModuleCapability(
	ctx context.Context, inspected *workcontext.Inspected, viewerAudience string,
) context.Context {
	if inspected == nil {
		return ctx
	}
	return context.WithValue(ctx, moduleCapabilityPairingKey{}, &moduleCapabilityPairing{
		audience:       inspected.Audience(),
		viewerAudience: viewerAudience,
		tenant:         inspected.TenantID(),
		session:        inspected.SessionID(),
	})
}

func presentedModuleCapability(ctx context.Context) *moduleCapabilityPairing {
	pairing, _ := ctx.Value(moduleCapabilityPairingKey{}).(*moduleCapabilityPairing)
	return pairing
}

// admitsConfinedCredential reports whether this capability may stand in for the
// confinement a resource-bound credential carries.
//
// Every comparison refuses an EMPTY value on either side before comparing. Two
// absent tenants or sessions are equal, and an equality that holds
// because neither side said anything is the whole failure mode: a capability
// signed without a session would otherwise pair with a credential that names
// none, and the binding would be satisfied by the absence of the thing it binds.
func (p *moduleCapabilityPairing) admitsConfinedCredential(claims *accessClaims) error {
	if p == nil {
		return fmt.Errorf("no capability was presented")
	}
	// A module's OWN identity capability (the module capability audience, minted
	// by StartModuleTask from the module's service credential) is verified at this
	// route and is not this: it says nothing about the person holding the bearer,
	// so pairing with it would admit a confined credential on the strength of a
	// capability another party minted for itself.
	if p.viewerAudience == "" || p.audience != p.viewerAudience {
		return fmt.Errorf("the capability presented is not one minted for this module on this person's behalf")
	}
	for _, binding := range []struct {
		name            string
		capability, jwt string
	}{
		{"organization", p.tenant, claims.OrgID},
		{"session", p.session, claims.SessionID},
	} {
		if binding.capability == "" || binding.jwt == "" || binding.capability != binding.jwt {
			// The refusal names WHICH binding failed, and no value from either
			// side. Which of the three disagreed is what a runtime author debugs
			// against; the values are a person's identifiers and a live session's.
			return fmt.Errorf(
				"the capability presented was not minted for the %s this credential names", binding.name)
		}
	}
	return nil
}
