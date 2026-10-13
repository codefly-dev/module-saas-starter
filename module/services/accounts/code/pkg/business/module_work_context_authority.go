package business

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A composed module reads the live authority behind the capabilities presented
// TO IT, so it can verify one at full strength rather than inspect it.
//
// The answers themselves are the ones this host's own issuer reads
// (work_context_seal.go); what lives here is the only question that is not the
// issuer's — WHO MAY ASK, AND ABOUT WHAT.
//
// This answers it as two values rather than as a verdict on request fields, and
// the difference is the whole lesson of the first revision. That version took
// the tenant and the audience as REQUEST fields and compared the audience to
// the caller's prefix. An independent review showed the comparison pinned
// nothing: no answer depended on either field, so a module could label the call
// with its own prefix and still read any principal's epoch, approved build or
// authorization revision in its tenant — a check that read as cover. What binds
// a question now is the SIGNED CAPABILITY it is about: the adapter authenticates
// it against the names below and answers only about the identifiers it carries.
//
//   - THE TENANT is the caller's own bound organization, taken from its verified
//     credential and never from a request. Every answer here is per
//     organization, and a module installed in several asks with the credential
//     it minted for the one in question.
//   - THE AUDIENCES are the caller's own names a capability may be addressed to.
//     An audience is what makes two capabilities signed by one key
//     non-interchangeable, so it is what decides whether a capability is this
//     module's to resolve authority for at all.

// ModuleWorkContextAuthorityRead is what one authorized module may ask about.
type ModuleWorkContextAuthorityRead struct {
	// Tenant is the organization every answer is for.
	Tenant string
	// Audiences are the caller's own names, in no significant order: its
	// registration prefix, and the route alias its delivered presence declares
	// when it declares one that is not already another clause's spelling.
	//
	// Both are the host's to derive — the prefix from the authority document,
	// the alias from delivered presence — so neither is a value a caller can
	// choose. A capability addressed to anything else is refused, including a
	// third name a declaration might give it: accepting that would mean
	// believing a caller's claim that an audience is its own.
	Audiences []string
}

// AuthorizeModuleWorkContextAuthorityRead authorizes one composed module's
// live-authority reads and reports the tenant and the audiences they are bound
// to.
//
// The caller must be a module principal whose capability decision stands right
// now — AuthorizeModuleCapability, as every path on this surface does, so a
// module whose installation was narrowed or withdrawn stops resolving authority
// at its next question rather than at its credential's expiry.
func (s *Service) AuthorizeModuleWorkContextAuthorityRead(
	ctx context.Context, caller ModuleCaller,
) (ModuleWorkContextAuthorityRead, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModuleWorkContextAuthorityRead{}, fenceErr
	}
	defer releaseAuthority()

	authority, _, err := s.authorizeModuleCapability(ctx, caller)
	if err != nil {
		return ModuleWorkContextAuthorityRead{}, err
	}
	grant := authority.Grant
	if caller.BoundOrg == "" || grant.Prefix == "" {
		return ModuleWorkContextAuthorityRead{}, status.Error(codes.PermissionDenied,
			"work context authority caller denied")
	}
	read := ModuleWorkContextAuthorityRead{
		Tenant:    caller.BoundOrg,
		Audiences: []string{grant.Prefix},
	}
	// The alias, from the same delivered presence the audience vocabulary is
	// derived from, and only the one belonging to THIS caller's registration:
	// the binding its grant was proven under. A capability addressed to a
	// module by its route alias is a shape this host mints — a binding's
	// declared `audience` is a name the authority document chooses — so leaving
	// the alias out would leave such a module on the forwarding-hop check with
	// no way to verify at full strength.
	//
	// A failed resolution is an error rather than a prefix-only answer, on the
	// same terms as HostAudiences: a store that cannot say what is declared
	// must not read as "this module has no alias", which is a posture question
	// decided by a lookup failure.
	alias, err := s.declaredModuleAlias(ctx, grant.PresenceBinding)
	if err != nil {
		return ModuleWorkContextAuthorityRead{}, err
	}
	if alias != "" {
		read.Audiences = append(read.Audiences, alias)
	}
	return read, nil
}

// declaredModuleAlias is the route alias delivered presence declares for one
// registry binding, or "" when that binding declares none, is not a module, or
// names a spelling another clause of the audience vocabulary owns.
//
// An EMPTY binding id resolves to no alias without a lookup. A grant with no
// presence binding must not match a declaration with none either: that would
// hand one module another's alias on the strength of two empty strings.
func (s *Service) declaredModuleAlias(ctx context.Context, presenceBinding string) (string, error) {
	if strings.TrimSpace(presenceBinding) == "" {
		return "", nil
	}
	bindings, err := s.liveDeclaredBindings(ctx)
	if err != nil {
		return "", err
	}
	for _, binding := range bindings {
		if binding.BindingID != presenceBinding || binding.Kind != SolutionDeclaredKindModule {
			continue
		}
		alias := strings.TrimSpace(binding.Alias)
		if alias == "" || alias == ModuleCapabilitiesAudience || alias == SourceOperationAudience ||
			strings.HasPrefix(alias, SolutionAudiencePrefix) {
			return "", nil
		}
		return alias, nil
	}
	return "", nil
}
