package adapters

import (
	"context"
	"slices"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	corescope "github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// A credential that is not a person's interactive session reaches a solution
// too: an API key, and a token an authorization grant bound to one solution's
// resource. Neither is ever forwarded to the solution. The gateway validates
// it, states what it verified under its own credential (who the credential
// acts as, the organization, the kind, a key's scopes, the resource a token is
// confined to, the installation that admitted the request), and asks Issue for
// the capability the solution is handed instead.
//
// What is issued is the credential's own grants at that solution, decided by
// the core as for a person:
//
//   - an API key carries its scopes within what its owner holds there through
//     their role on the installation. A scope the owner does not hold adds
//     nothing; a key whose scopes hold nothing there is refused. It is asked
//     for under no session of its owner's: no role view or cast narrows it,
//     and none is needed to be open.
//   - a resource-bound token carries what the person holds at the solution its
//     resource names, on its own open session, and is issued nothing at any
//     other audience.
//
// Both are issued at a solution alone, never at a module and never for a
// page's call or through a binding: a module is reached through the hop, by a
// person present. An impersonated session, and an identity acting on someone's
// behalf, stay refused whatever credential carries them.

// issueForStatedCredential issues for a stated credential. stated is false for
// a person's own interactive session, which the caller issues for.
func issueForStatedCredential(ctx context.Context, core *authority.Service, req *authorityv1.IssueRequest, r authority.IssueRequest) (stated bool, out authority.Issued, err error) {
	resource, confined := accountsauth.VerifiedResourceConfinement(ctx)
	key := credentialKindFromContext(ctx) == credentialKindAPIKey
	if !key && !confined {
		return false, authority.Issued{}, nil
	}
	const what = "be issued a capability"
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok || identity.EffectiveSubject == uuid.Nil {
		return true, out, status.Errorf(codes.Unauthenticated, "only the signed-in person may %s; the call carries no session", what)
	}
	if _, acting := accountsauth.VerifiedActorFromContext(ctx); acting || identity.Delegation != nil {
		return true, out, status.Errorf(codes.PermissionDenied, "an identity acting on someone's behalf (X-Act) cannot %s for them", what)
	}
	if identity.Impersonated() {
		return true, out, status.Errorf(codes.PermissionDenied, "an impersonated session cannot %s for the person it views as", what)
	}
	if identity.OrgID == uuid.Nil {
		return true, out, status.Error(codes.PermissionDenied, "the credential names no organization, so there is none to issue a capability in")
	}
	person := authority.Person{ID: identity.EffectiveSubjectID(), Organization: identity.OrgID.String()}
	refuse := func(code codes.Code, format string, args ...any) (bool, authority.Issued, error) {
		refusal := status.Errorf(code, format, args...)
		service.RecordAuthorityRefusal(ctx, authority.DecisionIssuePerson, person.Organization, person.ID, r.Audience, status.Convert(refusal).Message())
		return true, authority.Issued{}, refusal
	}
	if req.GetPageTarget() != "" || r.Binding != "" {
		return refuse(codes.PermissionDenied, "a stated credential is issued a capability at a solution itself, never for a page's call or through a binding")
	}
	if confined {
		if kind := credentialKindFromContext(ctx); kind != credentialKindSession {
			return refuse(codes.PermissionDenied, "a credential confined to a resource is a person's session; this call presented %q", kind)
		}
		origin, configured := accountsauth.ConfiguredPublicOrigin()
		if !configured {
			return refuse(codes.FailedPrecondition, "this host has no configured public address, so it cannot account for the resource this credential is confined to")
		}
		indicator, rerr := accountsauth.RequireResourceAtOrigin(resource, origin)
		if rerr != nil {
			return refuse(codes.PermissionDenied, "the credential is confined to a resource this host does not issue")
		}
		// The audience is the principal of the solution the resource names,
		// read from the core as it composes it: a token for one solution's
		// endpoint asks for nothing at another.
		if m, merr := service.ComposedAudience(ctx, r.Audience); merr != nil || m.Kind != domain.KindSolution || m.Name != indicator.SolutionID {
			return refuse(codes.PermissionDenied, "the credential is confined to one solution's resource and is issued a capability at that solution alone, which %q is not", r.Audience)
		}
		if _, admitted := accountsauth.VerifiedInstallation(ctx); !admitted {
			return refuse(codes.FailedPrecondition, "a confined credential reached this issue without the installation it was admitted under, so no consent to it was resolved")
		}
		person.SessionID = identity.SessionID.String()
		if err := requireOpenSession(ctx, person.SessionID); err != nil {
			return true, out, err
		}
	} else if m, merr := service.ComposedAudience(ctx, r.Audience); merr == nil && m.Kind != domain.KindSolution {
		// An audience the core does not compose is refused by the core below,
		// by its own name for it.
		return refuse(codes.PermissionDenied, "an API key is issued a capability only at a solution, which %q is not: a module is reached through the hop, by a person present", r.Audience)
	}
	if err := requireOrgMember(ctx, person.ID, person.Organization); err != nil {
		return true, out, err
	}
	ask := func(ctx context.Context, r authority.IssueRequest) (issued authority.Issued, err error) {
		err = oneRead(ctx, func(ctx context.Context) (err error) { issued, err = core.IssueForPerson(ctx, person, r); return err })
		return issued, err
	}
	if confined {
		out, err = ask(ctx, r)
		return true, out, err
	}
	// The key's ceiling is what its owner holds here. The core is asked for
	// that whole holding first, as a probe whose answer is not handed out; a
	// refusal of it is the key's own, asked again as itself so the journal is
	// told of it.
	whole, err := ask(business.AuthorityProbe(ctx), authority.IssueRequest{Audience: r.Audience, Organization: r.Organization})
	if err != nil {
		if _, err = ask(ctx, r); err == nil {
			err = status.Error(codes.Aborted, "what the key's owner holds changed while the key was issued for; ask again")
		}
		return true, out, err
	}
	granted := scopesAKeyCarries(whole.Context.GetAuthorityScopes(), scopesFromContext(ctx))
	if len(granted) == 0 {
		return refuse(codes.PermissionDenied, "the key's scopes hold nothing at %s: it carries its scopes within what its owner holds there, and none of them is held", r.Audience)
	}
	if len(r.Scopes) == 0 {
		r.Scopes = granted
	} else {
		// An ask is held to what the key carries, owner aside: it names no
		// owner, and takes the one the holding selects.
		if !corescope.Attenuates(corescope.Unowned(r.Scopes), corescope.Unowned(granted)) {
			return refuse(codes.PermissionDenied, "asked beyond what the key carries at %s", r.Audience)
		}
		r.Scopes = corescope.Intersect(granted, r.Scopes)
	}
	out, err = ask(ctx, r)
	return true, out, err
}

// scopesAKeyCarries is held cut to the actions a key's scopes name: each
// "<resource kind>:<action>" of the key, wildcards honoured as everywhere a
// key is held to its scopes (scopeMatches), keeps that action of that kind,
// on the owner and the resources the holding selects. A key scope naming what
// is not held adds nothing.
func scopesAKeyCarries(held []*basev0.WorkScopeV1, keyScopes []string) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, sc := range held {
		actions := slices.DeleteFunc(slices.Clone(sc.GetActions()), func(action string) bool {
			return !grantsScope(keyScopes, sc.GetResourceKind()+":"+action)
		})
		if len(actions) == 0 {
			continue
		}
		kept, _ := proto.Clone(sc).(*basev0.WorkScopeV1)
		kept.Actions = actions
		out = append(out, kept)
	}
	return out
}
