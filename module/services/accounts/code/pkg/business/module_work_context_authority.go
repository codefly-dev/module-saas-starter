package business

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A composed module reads the live authority behind the capabilities presented
// TO IT, so it can verify one at full strength rather than inspect it.
//
// The answers themselves are the ones this host's own issuer reads
// (work_context_seal.go); what lives here is the only question that is not the
// issuer's — WHO MAY ASK. A module may ask about a capability addressed to
// itself, in the tenant its own credential is bound to, and about nothing else.
//
// Both halves are load-bearing and neither is a privacy nicety:
//
//   - THE TENANT. Every answer here is per organization, and a module's
//     credential is bound to one. A module that could name another tenant would
//     be reading one organization's revocation state while installed in a
//     different one. A module installed in several (a cross-tenant grant) asks
//     with the credential it minted for the organization in question, exactly as
//     every other capability it exercises there does.
//   - THE AUDIENCE. An audience is what makes two capabilities signed by one key
//     non-interchangeable, and a declared module's audience is its registration
//     prefix. Serving live authority for another module's audience would let this
//     caller VERIFY — at full strength, with every seal check passing — a
//     capability a person minted for somebody else. Pinning the audience to the
//     caller's own prefix is what keeps the strongest check in the model from
//     being the one that admits the wrong consumer.

// AuthorizeModuleWorkContextAuthorityRead authorizes one live-authority read by
// a composed module: the caller must be a module principal whose capability
// decision stands right now (AuthorizeModuleCapability, as every path here
// does), orgID must be the tenant its credential is bound to, and audience must
// be its own registration prefix.
//
// It returns nothing but a verdict. The values a verifier compares against are
// the issuer's own reads, which the adapter serves through the one set of
// core-typed sources it mints and verifies with; this decides only whether this
// caller may see them for this (tenant, audience).
func (s *Service) AuthorizeModuleWorkContextAuthorityRead(
	ctx context.Context, caller ModuleCaller, orgID, audience string,
) error {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()

	grant, err := s.moduleCapability(ctx, caller)
	if err != nil {
		return err
	}
	if err := authorizeTenant(caller, grant, orgID); err != nil {
		return err
	}
	// One refusal for both remaining shapes, deliberately indistinguishable: a
	// caller probing this surface learns whether it may ask, never whether
	// another module's prefix exists.
	if orgID == "" || grant.Prefix == "" || audience != grant.Prefix {
		return status.Error(codes.PermissionDenied, "work context authority caller denied")
	}
	return nil
}
