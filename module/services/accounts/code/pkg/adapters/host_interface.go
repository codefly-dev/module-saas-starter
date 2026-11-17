package adapters

// The host interfaces (codefly.dev/authority, composition, installation,
// session) served by accounts. Every decision is the shared authority core's
// (codefly-dev/authority libraries/authority-core), the same core the local
// authority serves on a laptop, over this host's store (pkg/infra
// postgres_authority_core.go). What this file adds is the wire and what is
// this host's own: who the caller is (the session the perimeter verified, or
// a workload credential the execution reviewer reviewed), the signing key
// Keys publishes, delivery (composition.go's ApplyComposition) and the audit
// record of each issue.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// authorityCore is the core every host interface call is decided by.
func authorityCore() (*authority.Service, error) {
	if service == nil {
		return nil, status.Error(codes.Unavailable, business.ErrAuthorityCoreUnconfigured.Error())
	}
	core, err := service.AuthorityCore()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return core, nil
}

// coreStatus maps a refusal to a status naming its reason: every refusal
// reaches the caller. The core's refusals carry their code; core's Work
// Context errors map by kind.
func coreStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, business.ErrAuthorityDecisionUnrecorded) {
		// An admitted issue the journal did not take: nothing was handed
		// out, and the caller asks again.
		return status.Error(codes.Unavailable, business.ErrAuthorityDecisionUnrecorded.Error())
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	if c, ok := domain.CodeOf(err); ok {
		return status.Error(map[domain.Code]codes.Code{
			domain.Denied: codes.PermissionDenied, domain.NotFound: codes.NotFound,
			domain.Invalid: codes.InvalidArgument, domain.Precondition: codes.FailedPrecondition,
		}[c], err.Error())
	}
	switch {
	case errors.Is(err, workcontext.ErrNoBinding):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, workcontext.ErrRevoked), errors.Is(err, workcontext.ErrNoSeal):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, workcontext.ErrInvalid), errors.Is(err, workcontext.ErrNotACoreToken), errors.Is(err, workcontext.ErrReplayed):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, authority.ErrConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, business.ErrAuthorityCoreUnconfigured):
		return status.Error(codes.Unavailable, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// signedInPerson is the person a session bearer authenticates, as the core
// takes them: their id, the organization the session is at, the session,
// which is open (requireOpenSession).
// Only an interactive session acts as a person here: never a credential
// confined to one consumer's ceiling, an identity acting on someone's behalf
// (X-Act), an administrator viewing as someone (impersonation) or an API key.
func signedInPerson(ctx context.Context, what string) (authority.Person, error) {
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok || identity.EffectiveSubject == uuid.Nil {
		return authority.Person{}, status.Errorf(codes.Unauthenticated, "only the signed-in person may %s; the call carries no session", what)
	}
	if _, confined := accountsauth.VerifiedResourceConfinement(ctx); confined {
		return authority.Person{}, status.Errorf(codes.PermissionDenied, "a resource-confined credential cannot %s: it acts only within its consumer's ceiling", what)
	}
	if _, acting := accountsauth.VerifiedActorFromContext(ctx); acting || identity.Delegation != nil {
		return authority.Person{}, status.Errorf(codes.PermissionDenied, "an identity acting on someone's behalf (X-Act) cannot %s for them", what)
	}
	if identity.Impersonated() {
		return authority.Person{}, status.Errorf(codes.PermissionDenied, "an impersonated session cannot %s for the person it views as", what)
	}
	if kind := credentialKindFromContext(ctx); kind != credentialKindSession {
		if kind == "" {
			kind = "no credential kind"
		}
		return authority.Person{}, status.Errorf(codes.PermissionDenied, "only an interactive session may %s; this call presented %s", what, kind)
	}
	person := authority.Person{ID: identity.EffectiveSubjectID(), SessionID: identity.SessionID.String()}
	if identity.OrgID != uuid.Nil {
		person.Organization = identity.OrgID.String()
	}
	// The session is read from this host's own record, not from the bearer
	// alone: one that was ended (End, a sign-out, a revocation) or is past the
	// moment it ends on its own is unauthenticated here, whatever the
	// perimeter's revocation markers still say.
	if err := requireOpenSession(ctx, person.SessionID); err != nil {
		return authority.Person{}, err
	}
	return person, nil
}

// requireOpenSession refuses, Unauthenticated, a session id that names no open
// session of this host.
func requireOpenSession(ctx context.Context, sessionID string) error {
	if service == nil {
		return status.Error(codes.Unavailable, "this host reads no sessions")
	}
	_, err := service.SessionEndsAt(ctx, sessionID)
	if errors.Is(err, business.ErrNoOpenSession) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot read whether the session is open: %v", err)
	}
	return nil
}

// workload is the composed module a workload credential proves, reviewed by
// the execution reviewer and resolved to the one module whose delivered
// manifest declares that workload, bound to the organization the module
// envelope pins it to: a pinned workload is issued nothing elsewhere. A shared
// one acts only where an administrator installed it (the core reads accounts'
// installations).
func workload(ctx context.Context, credential, which string) (authority.Workload, error) {
	if service == nil {
		return authority.Workload{}, status.Error(codes.Unavailable, "the execution reviewer is not wired")
	}
	module, err := service.ModulePrefixOfWorkload(ctx, credential)
	if err != nil {
		// ModulePrefixOfWorkload has logged the refusal, redacted; it is
		// answered here without a second line.
		refusal, _ := classifyCredentialError(ctx, err, which)
		return authority.Workload{}, refusal
	}
	tenant, err := service.ModuleTenant(ctx, module)
	if err != nil {
		return authority.Workload{}, status.Errorf(codes.Unavailable, "the module registry could not be read current: %v", err)
	}
	return authority.Workload{Module: module, Organization: tenant}, nil
}

// HostAuthorityServer serves codefly.dev/authority.
type HostAuthorityServer struct {
	authorityv1.UnimplementedAuthorityServiceServer
	workContexts *WorkContextAuthorityServer
	// admissions registers and publishes the hop's admission keys; nil reads
	// the service.
	admissions admissionKeys
}

// NewHostAuthorityServer serves the authority interface with the Work Context
// key workContexts holds.
func NewHostAuthorityServer(workContexts *WorkContextAuthorityServer) *HostAuthorityServer {
	return &HostAuthorityServer{workContexts: workContexts}
}

// Keys is the issuer and the one Work Context signing key the core signs
// with, and apart from it the admission keys the hop registered.
func (s *HostAuthorityServer) Keys(ctx context.Context, _ *authorityv1.KeysRequest) (*authorityv1.KeysResponse, error) {
	if !s.workContexts.configured() {
		return nil, status.Error(codes.FailedPrecondition, ErrWorkContextAuthorityUnconfigured.Error())
	}
	admission, err := s.publishedAdmissionKeys(ctx)
	if err != nil {
		return nil, err
	}
	return &authorityv1.KeysResponse{Issuer: s.workContexts.issuer, Keys: []*authorityv1.PublicKey{{
		KeyId: s.workContexts.keyID, Algorithm: workcontext.Algorithm, PublicKey: []byte(s.workContexts.publicKey),
		Purpose: workContextKeyPurpose,
	}}, AdmissionKeys: admission}, nil
}

// oneRead answers one authority question with every read the core makes for
// it taken from one transaction (business.Service.WithAuthoritySnapshot): a
// module asks these in bursts, and read source by source each one took the
// control-plane pool a dozen times. Only the core's decision runs inside;
// authenticating the caller and recording the issue stay outside it.
func oneRead(ctx context.Context, ask func(context.Context) error) error {
	return service.WithAuthoritySnapshot(ctx, ask)
}

// Check verifies a capability for the audience the caller is and, when a
// method is named, applies what the audience's delivery declares for it.
func (s *HostAuthorityServer) Check(ctx context.Context, req *authorityv1.CheckRequest) (*authorityv1.CheckResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	var verified *workcontext.Verified
	var revision uint64
	var refused error
	err = oneRead(ctx, func(ctx context.Context) (err error) {
		verified, revision, refused, err = core.CheckMethod(ctx, req.GetToken(), req.GetAudience(), req.GetMethod(), req.GetAdvisory())
		return err
	})
	if err != nil {
		return nil, coreStatus(err)
	}
	wire, err := workContextToWire(verified.Context())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &authorityv1.CheckResponse{Context: wire, AuthorizationRevision: revision}
	if refused != nil {
		// The advisory answer names the refusal exactly as an enforced check reports it.
		out.Refusal = status.Convert(coreStatus(refused)).Message()
	}
	return out, nil
}

// Issue signs a capability for one owner shape. Each shape is authenticated
// first (the session, the workload credential, the actor's credential). The
// core tells this host's journal what it decided before it answers
// (business's decision hook): an admitted issue is recorded before the
// capability is handed out, and a refused one with what was asked and why.
func (s *HostAuthorityServer) Issue(ctx context.Context, req *authorityv1.IssueRequest) (*authorityv1.IssueResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	if req.GetTtlSeconds() < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "a lifetime is not negative: %d seconds", req.GetTtlSeconds())
	}
	r := authority.IssueRequest{Audience: req.GetAudience(), Binding: req.GetOperationBindingId(), Operation: req.GetOperation(),
		Organization: req.GetOrganizationId(), Scopes: scopesFromWire(req.GetScopes()), TTL: time.Duration(req.GetTtlSeconds()) * time.Second}
	var out authority.Issued
	switch as := req.GetAs().(type) {
	case *authorityv1.IssueRequest_Person:
		person, perr := signedInPerson(ctx, "be issued a capability")
		if perr != nil {
			return nil, perr
		}
		if err := requireOrgMember(ctx, person.ID, person.Organization); err != nil {
			return nil, err
		}
		// A page's call on the module edge goes through its target's
		// binding to the audience when the target declares one.
		if page := req.GetPageTarget(); page != "" {
			if err := service.RequireModuleEdge(ctx, req.GetActor().GetCredential()); err != nil {
				if errors.Is(err, business.ErrNotTheModuleEdge) {
					return nil, status.Error(codes.PermissionDenied, err.Error())
				}
				return nil, credentialRefusal(ctx, err, "the actor", req.GetActor().GetCredential())
			}
			// Through a binding it is the target's hop, which the journal
			// records as a child.
			err = oneRead(ctx, func(ctx context.Context) (err error) { out, err = core.IssueForPage(ctx, person, page, r); return err })
		} else {
			err = oneRead(ctx, func(ctx context.Context) (err error) { out, err = core.IssueForPerson(ctx, person, r); return err })
		}
	case *authorityv1.IssueRequest_Workload:
		w, werr := workload(ctx, as.Workload.GetCredential(), "workload")
		if werr != nil {
			return nil, werr
		}
		err = oneRead(ctx, func(ctx context.Context) (err error) { out, err = core.IssueForWorkload(ctx, w, r); return err })
	case *authorityv1.IssueRequest_Installation:
		w, werr := workload(ctx, req.GetActor().GetCredential(), "actor")
		if werr != nil {
			return nil, werr
		}
		err = oneRead(ctx, func(ctx context.Context) (err error) {
			out, err = core.IssueForInstallation(ctx, w, as.Installation.GetInstallationId(), r)
			return err
		})
	case *authorityv1.IssueRequest_ParentToken:
		w, werr := workload(ctx, req.GetActor().GetCredential(), "actor")
		if werr != nil {
			return nil, werr
		}
		err = oneRead(ctx, func(ctx context.Context) (err error) {
			out, err = core.IssueChild(ctx, as.ParentToken, w, r)
			return err
		})
	case *authorityv1.IssueRequest_Delegation:
		w, werr := workload(ctx, req.GetActor().GetCredential(), "actor")
		if werr != nil {
			return nil, werr
		}
		err = oneRead(ctx, func(ctx context.Context) (err error) {
			out, err = core.IssueForDelegation(ctx, as.Delegation.GetDelegationId(), w, r)
			return err
		})
	default:
		return nil, status.Error(codes.InvalidArgument, "an issue names whom it is for: a person, a workload, an installation, a parent token or a delegation")
	}
	if err != nil {
		return nil, coreStatus(err)
	}
	return s.issued(out)
}

// issued answers an issue the core admitted. It is recorded already: the core
// told this host's journal before it answered (business's decision hook), and
// an issue the journal did not take never reaches here.
func (s *HostAuthorityServer) issued(out authority.Issued) (*authorityv1.IssueResponse, error) {
	wc := out.Context
	wire, err := workContextToWire(wc)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &authorityv1.IssueResponse{Token: out.Token, ExpiresAtUnix: wc.GetExpiresAtUnix(), Context: wire}, nil
}

// ListResources answers which resources of one kind a checked capability
// covers for one action, decided now.
func (s *HostAuthorityServer) ListResources(ctx context.Context, req *authorityv1.ListResourcesRequest) (*authorityv1.ListResourcesResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	var grants []authority.Grant
	err = oneRead(ctx, func(ctx context.Context) (err error) {
		grants, err = core.ListResources(ctx, req.GetToken(), req.GetAudience(), req.GetResourceKind(), req.GetAction())
		return err
	})
	if err != nil {
		return nil, coreStatus(err)
	}
	out := &authorityv1.ListResourcesResponse{}
	for _, g := range grants {
		grant := &authorityv1.ResourceGrant{ResourceOwnerId: g.Owner}
		switch {
		case g.All:
			grant.Match = &authorityv1.ResourceGrant_All{All: true}
		case g.ID != "":
			grant.Match = &authorityv1.ResourceGrant_Id{Id: g.ID}
		case g.Prefix != "":
			grant.Match = &authorityv1.ResourceGrant_Prefix{Prefix: g.Prefix}
		default:
			grant.Match = &authorityv1.ResourceGrant_Pattern{Pattern: g.Pattern}
		}
		out.Grants = append(out.Grants, grant)
	}
	return out, nil
}

// Delegate records the signed-in person's grant to a module, within what they hold.
func (s *HostAuthorityServer) Delegate(ctx context.Context, req *authorityv1.DelegateRequest) (*authorityv1.Delegation, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	person, err := signedInPerson(ctx, "delegate")
	if err != nil {
		return nil, err
	}
	d, err := core.Delegate(ctx, person, req.GetModule(), scopesFromWire(req.GetScopes()), req.GetPurpose(), req.GetTtlSeconds())
	if err != nil {
		return nil, coreStatus(err)
	}
	return delegationToWire(d, core.Now()), nil
}

// RevokeDelegation ends one of the signed-in person's delegations.
func (s *HostAuthorityServer) RevokeDelegation(ctx context.Context, req *authorityv1.RevokeDelegationRequest) (*authorityv1.RevokeDelegationResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	person, err := signedInPerson(ctx, "revoke a delegation")
	if err != nil {
		return nil, err
	}
	d, err := core.RevokeDelegation(ctx, person, req.GetDelegationId())
	if err != nil {
		return nil, coreStatus(err)
	}
	return &authorityv1.RevokeDelegationResponse{Delegation: delegationToWire(d, core.Now())}, nil
}

// GetDelegation reads one delegation for its module (by its workload
// credential) or its grantor (by their session); anyone else learns nothing.
func (s *HostAuthorityServer) GetDelegation(ctx context.Context, req *authorityv1.GetDelegationRequest) (*authorityv1.Delegation, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	var d domain.Delegation
	if credential := req.GetActor().GetCredential(); credential != "" {
		w, werr := workload(ctx, credential, "actor")
		if werr != nil {
			return nil, werr
		}
		d, err = core.Delegation(ctx, req.GetDelegationId(), nil, &w)
	} else {
		person, perr := signedInPerson(ctx, "read a delegation")
		if perr != nil {
			return nil, perr
		}
		d, err = core.Delegation(ctx, req.GetDelegationId(), &person, nil)
	}
	if err != nil {
		return nil, coreStatus(err)
	}
	return delegationToWire(d, core.Now()), nil
}

// AttachResource records a resource created through the installation the
// presented capability was issued through; the kind's serving module asks.
func (s *HostAuthorityServer) AttachResource(ctx context.Context, req *authorityv1.AttachResourceRequest) (*authorityv1.AttachResourceResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	w, err := workload(ctx, req.GetActor().GetCredential(), "actor")
	if err != nil {
		return nil, err
	}
	id, err := core.AttachResource(ctx, authority.AttachmentRequest{Token: req.GetToken(), Kind: req.GetResourceKind(), ResourceID: req.GetResourceId(), Store: w})
	if err != nil {
		return nil, coreStatus(err)
	}
	return &authorityv1.AttachResourceResponse{InstallationId: id}, nil
}

// DetachResource releases a resource its serving module deleted.
func (s *HostAuthorityServer) DetachResource(ctx context.Context, req *authorityv1.DetachResourceRequest) (*authorityv1.DetachResourceResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	w, err := workload(ctx, req.GetActor().GetCredential(), "actor")
	if err != nil {
		return nil, err
	}
	id, err := core.DetachResource(ctx, w, req.GetOrganizationId(), req.GetResourceKind(), req.GetResourceId())
	if err != nil {
		return nil, coreStatus(err)
	}
	return &authorityv1.DetachResourceResponse{InstallationId: id}, nil
}

// AuthorizeTransfer decides whether an existing resource joins a solution
// principal, and records it; the kind's serving module asks with the person's
// capability.
func (s *HostAuthorityServer) AuthorizeTransfer(ctx context.Context, req *authorityv1.AuthorizeTransferRequest) (*authorityv1.AuthorizeTransferResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	w, err := workload(ctx, req.GetActor().GetCredential(), "actor")
	if err != nil {
		return nil, err
	}
	d, err := core.AuthorizeTransfer(ctx, authority.TransferRequest{Token: req.GetToken(), Store: w, Kind: req.GetResourceKind(),
		ResourceID: req.GetResourceId(), From: req.GetFromOwnerId(), To: req.GetToPrincipalId()})
	if err != nil {
		return nil, coreStatus(err)
	}
	return &authorityv1.AuthorizeTransferResponse{PrincipalId: d.PrincipalID, AuthorizedBy: d.AuthorizedBy}, nil
}

// HostInstallationServer serves codefly.dev/installation.
type HostInstallationServer struct {
	installationv1.UnimplementedInstallationServiceServer
}

// Current reads only the authenticated module's own active installation, in
// the tenant of a current parent addressed to it: the module proves itself by
// its workload credential and the core verifies the parent. It needs no
// session and exposes no roles or grants.
func (s *HostInstallationServer) Current(ctx context.Context, req *installationv1.CurrentInstallationRequest) (*installationv1.CurrentInstallation, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	// Current answers a module about itself alone, so a credential that proves
	// no module is refused the read. A credential that could not be read is
	// answered as that failure: nothing was decided about the caller.
	actor, err := workload(ctx, req.GetActorCredential(), "the actor")
	if err != nil {
		return nil, refusedUnlessFailed(err)
	}
	// The namespace is where the parent authors: the installation's own, or,
	// for a parent owned by a solution installation, that installation's.
	inst, namespace, err := core.CurrentAuthoring(ctx, req.GetParentToken(), actor)
	if err != nil {
		return nil, coreStatus(err)
	}
	return &installationv1.CurrentInstallation{Id: inst.ID, OrganizationId: inst.Organization, Target: inst.Target,
		Revision: inst.Revision, Namespace: namespace}, nil
}

// Review reads the exact authority offer an administrator consents to,
// without recording consent; a module page's update is reviewed by its
// installer-role holder too. The core decides who (its approver), under the
// caller's session.
func (s *HostInstallationServer) Review(ctx context.Context, req *installationv1.ReviewRequest) (*installationv1.ConsentApproval, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, "review an installation")
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, caller.ID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	a, err := core.Review(ctx, caller, req.GetOrganizationId(), req.GetTarget())
	if err != nil {
		return nil, coreStatus(err)
	}
	return &installationv1.ConsentApproval{TargetId: a.TargetID, Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, Digest: a.Digest, ReachDigest: a.ReachDigest}, nil
}

// Install installs a solution at the caller's organization under the consent
// they reviewed; nobody holds a role on it until granted one. A module page's
// update is approved here too, by an administrator or the page's
// installer-role holder, as the core decides (its approver).
func (s *HostInstallationServer) Install(ctx context.Context, req *installationv1.InstallRequest) (*installationv1.Installation, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, "install")
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, caller.ID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	var approval *domain.ConsentApproval
	if a := req.GetApproval(); a != nil {
		approval = &domain.ConsentApproval{TargetID: a.GetTargetId(), Generation: a.GetGeneration(), ExpectedRevision: a.GetExpectedRevision(),
			OperationID: a.GetOperationId(), Digest: a.GetDigest(), ReachDigest: a.GetReachDigest()}
	}
	inst, err := service.InstallAuthoritySolution(ctx, caller, req.GetOrganizationId(), req.GetTarget(), req.GetRole(), req.GetOwnerPersonId(), approval)
	if err != nil {
		return nil, coreStatus(err)
	}
	return installationToWire(core, ctx, inst), nil
}

// GrantRole grants a person or a team one of the solution's roles.
func (s *HostInstallationServer) GrantRole(ctx context.Context, req *installationv1.GrantRoleRequest) (*installationv1.Installation, error) {
	return s.change(ctx, "grant a role", (*authority.Service).GrantRole, authority.RoleRequest{Organization: req.GetOrganizationId(),
		InstallationID: req.GetInstallationId(), Role: req.GetRole(), PersonID: req.GetPersonId(), TeamID: req.GetTeamId()})
}

// RevokeRole ends one grant; its subject's capabilities through the installation end with it.
func (s *HostInstallationServer) RevokeRole(ctx context.Context, req *installationv1.RevokeRoleRequest) (*installationv1.Installation, error) {
	return s.change(ctx, "revoke a role", (*authority.Service).RevokeRole, authority.RoleRequest{Organization: req.GetOrganizationId(),
		InstallationID: req.GetInstallationId(), Role: req.GetRole(), PersonID: req.GetPersonId(), TeamID: req.GetTeamId()})
}

func (s *HostInstallationServer) change(ctx context.Context, what string, do func(*authority.Service, context.Context, authority.Person, authority.RoleRequest) (domain.Installation, error), r authority.RoleRequest) (*installationv1.Installation, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, what)
	if err != nil {
		return nil, err
	}
	inst, err := do(core, ctx, caller, r)
	if err != nil {
		return nil, coreStatus(err)
	}
	return installationToWire(core, ctx, inst), nil
}

// ListRoleGrants answers who holds which role on an installation.
func (s *HostInstallationServer) ListRoleGrants(ctx context.Context, req *installationv1.ListRoleGrantsRequest) (*installationv1.ListRoleGrantsResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, "list role grants")
	if err != nil {
		return nil, err
	}
	grants, err := core.ListRoleGrants(ctx, caller, req.GetOrganizationId(), req.GetInstallationId())
	if err != nil {
		return nil, coreStatus(err)
	}
	out := &installationv1.ListRoleGrantsResponse{}
	for _, g := range grants {
		out.Grants = append(out.Grants, &installationv1.RoleGrant{Role: g.Role, PersonId: g.PersonID, TeamId: g.TeamID})
	}
	return out, nil
}

// List answers what one organization has installed, for its administrator.
func (s *HostInstallationServer) List(ctx context.Context, req *installationv1.ListRequest) (*installationv1.ListResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, "list installations")
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, caller.ID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	all, err := core.Installations(ctx, caller, req.GetOrganizationId())
	if err != nil {
		return nil, coreStatus(err)
	}
	slices.SortFunc(all, func(a, b domain.Installation) int { return strings.Compare(a.ID, b.ID) })
	out := &installationv1.ListResponse{}
	for _, inst := range all {
		out.Installations = append(out.Installations, installationToWire(core, ctx, inst))
	}
	return out, nil
}

// ListHeld answers the signed-in person which installations of their
// organization they may use: each active one they hold a role on, granted to
// them or to a team of theirs, with those roles. It is their own navigation,
// so it names no other holder and refuses nobody who simply holds nothing:
// the core decides, reading their teams from the store rather than from the
// session, so leaving a team drops an entry at once.
func (s *HostInstallationServer) ListHeld(ctx context.Context, req *installationv1.ListHeldRequest) (*installationv1.ListHeldResponse, error) {
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	caller, err := signedInPerson(ctx, "list the installations they hold a role on")
	if err != nil {
		return nil, err
	}
	held, err := core.HeldInstallations(ctx, caller, req.GetOrganizationId())
	if err != nil {
		return nil, coreStatus(err)
	}
	out := &installationv1.ListHeldResponse{}
	for _, h := range held {
		out.Installations = append(out.Installations, &installationv1.HeldInstallation{
			Installation: installationToWire(core, ctx, h.Installation), Roles: h.Roles,
		})
	}
	return out, nil
}

// installationToWire is an installation as the interface states it: the roles
// its holders may hold, from the declaration that governs them
// (authority.ApprovedDeclaration, the core's own rule), and upgrade_available
// as the core answers it (Service.UpgradeAvailable): the target delivered
// past its approved generation, or what its roles reach in the composition
// now widened past the approved reach. An installation whose approved
// declaration the core refuses lists no role: the core refuses its holders.
func installationToWire(core *authority.Service, ctx context.Context, i domain.Installation) *installationv1.Installation {
	approved, err := core.ApprovedDeclaration(ctx, i)
	if err != nil {
		approved = domain.Module{}
	}
	upgrade, err := core.UpgradeAvailable(ctx, i)
	if err != nil {
		upgrade = false
	}
	out := installationWire(i, approved, upgrade)
	// What its declared reach the composition leaves unreached, as the core
	// answers it (Service.Unreached): a readiness condition beside
	// upgrade_available; a read it cannot make answers none.
	out.Unreached, _ = core.Unreached(ctx, i)
	return out
}

// installationWire converts one snapshot beside the declaration its consent
// approved and the core's upgrade_available, without another authority
// read: the roles are approved's.
func installationWire(i domain.Installation, approved domain.Module, upgrade bool) *installationv1.Installation {
	st := map[domain.Status]installationv1.Installation_Status{
		domain.Pending: installationv1.Installation_STATUS_PENDING, domain.Active: installationv1.Installation_STATUS_ACTIVE, domain.Revoked: installationv1.Installation_STATUS_REVOKED,
	}[i.Status]
	out := &installationv1.Installation{Id: i.ID, OrganizationId: i.Organization, Target: i.Target, Status: st, Revision: i.Revision,
		Role: i.AgentRole, PrincipalId: i.PrincipalID, Namespace: i.Organization + "." + i.Target,
		ApprovedGeneration: i.Consent.Generation, UpgradeAvailable: upgrade}
	for name := range approved.Roles {
		out.Roles = append(out.Roles, name)
	}
	slices.Sort(out.Roles)
	return out
}

func delegationToWire(d domain.Delegation, now time.Time) *authorityv1.Delegation {
	out := &authorityv1.Delegation{DelegationId: d.ID, OrganizationId: d.Organization, Grantor: d.Grantor, Module: d.Module, Purpose: d.Purpose,
		Active: d.Active(now), Scopes: scopesToWire(d.Scopes)}
	if !d.ExpiresAt.IsZero() {
		out.ExpiresAtUnix = d.ExpiresAt.Unix()
	}
	return out
}

// scopesFromWire converts interface scopes to core's, byte for byte.
func scopesFromWire(in []*authorityv1.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		raw, _ := proto.Marshal(sc)
		c := &basev0.WorkScopeV1{}
		_ = proto.Unmarshal(raw, c)
		out = append(out, c)
	}
	return out
}

// scopesToWire converts core's scopes to the interface's, byte for byte.
func scopesToWire(in []*basev0.WorkScopeV1) []*authorityv1.WorkScopeV1 {
	out := make([]*authorityv1.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		raw, _ := proto.Marshal(sc)
		c := &authorityv1.WorkScopeV1{}
		_ = proto.Unmarshal(raw, c)
		out = append(out, c)
	}
	return out
}

func workContextToWire(in proto.Message) (*authorityv1.WorkContextV1, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		return nil, err
	}
	out := &authorityv1.WorkContextV1{}
	return out, proto.Unmarshal(raw, out)
}

// credentialRefusal names why a workload credential did not prove a module,
// always with the reason:
//
//   - the host could not establish what the caller runs: Unavailable, nothing
//     was decided;
//   - no credential, or the module's identity refused as itself (the pod it
//     names no longer matches what its principal declares, its tenant moved):
//     Unauthenticated, so the module drops what it holds and mints again, as
//     the module tier answers it;
//   - a token the review refused outright (forged, unknown): PermissionDenied,
//     the code the shared conformance suite holds a forged workload credential
//     to;
//   - a workload declared by more than one principal: PermissionDenied, a
//     composition fault no re-mint repairs.
//
// A credential that could not be read is never refused: the request ended
// (Canceled), its deadline passed (DeadlineExceeded), the review could not run
// (Unavailable), or anything else (Internal). Nothing about the caller was
// decided, and PermissionDenied would tell it its credential proves no module.
func credentialRefusal(ctx context.Context, err error, which, credential string) error {
	refusal, failed := classifyCredentialError(ctx, err, which)
	if failed {
		logCredentialReadFailure(ctx, err, which, credential, status.Code(refusal))
	}
	return refusal
}

// classifyCredentialError is credentialRefusal's answer without its log line,
// and whether the credential could not be read (a failure, not a verdict). A
// failure is answered with what failed and the code: the reviewer's own error
// can carry the API server's address or a transport error, and an in-mesh
// caller may relay what it is told.
//
// Whether the caller ended its request is the caller's context's to say, as
// the gateway decides it: a dependency answering Canceled while the caller is
// live is a review that could not run, Unavailable.
func classifyCredentialError(ctx context.Context, err error, which string) (error, bool) {
	// A verdict first, whatever became of the caller: a refused credential
	// is a refusal, logged at WARN, even when the caller has since ended its
	// request; otherwise a prober that cancels every call keeps its forged
	// credentials out of WARN.
	switch {
	case errors.Is(err, business.ErrExecutionNotBoundToken), errors.Is(err, business.ErrModuleIdentityDenied):
		return status.Errorf(codes.Unauthenticated, "%s credential refused: %v", which, err), false
	case errors.Is(err, business.ErrExecutionTokenRejected):
		return status.Errorf(codes.PermissionDenied, "%s credential refused: %v", which, business.ErrExecutionTokenRejected), false
	case errors.Is(err, business.ErrExecutionWorkloadAmbiguous):
		return status.Errorf(codes.PermissionDenied, "%s credential refused: %v", which, err), false
	}
	switch ended := ctx.Err(); {
	case errors.Is(ended, context.Canceled):
		return status.Errorf(codes.Canceled, "%s credential could not be read: the request ended", which), true
	case errors.Is(ended, context.DeadlineExceeded):
		return status.Errorf(codes.DeadlineExceeded, "%s credential could not be read in time", which), true
	}
	switch {
	case errors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return status.Errorf(codes.Unavailable, "%s credential could not be reviewed: the review could not run", which), true
	case errors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return status.Errorf(codes.DeadlineExceeded, "%s credential could not be read in time", which), true
	case errors.Is(err, business.ErrExecutionUnbound):
		return status.Errorf(codes.Unavailable, "%s credential could not be reviewed: the review could not run", which), true
	}
	return status.Errorf(codes.Internal, "%s credential could not be read", which), true
}

// logCredentialReadFailure logs the reviewer's error for a credential that
// could not be read, with the credential redacted as ModulePrefixOfWorkload
// redacts it. A caller ending its own request carries no operational signal,
// so it is logged below WARN.
func logCredentialReadFailure(ctx context.Context, err error, which, credential string, code codes.Code) {
	reason := business.RedactWorkloadCredential(err.Error(), credential)
	log := wool.Get(ctx).In("credentialRefusal")
	fields := []*wool.LogField{wool.Field("credential", which), wool.Field("code", code.String()), wool.Field("reason", reason)}
	if code == codes.Canceled {
		log.Info("a workload credential could not be read", fields...)
		return
	}
	log.Warn("a workload credential could not be read", fields...)
}

// refusedUnlessFailed is a workload-credential error as a method that refuses
// any credential proving no module answers it: a verdict (Unauthenticated,
// PermissionDenied) is PermissionDenied with its reason, and a credential
// that could not be read keeps the failure's own code.
func refusedUnlessFailed(err error) error {
	switch status.Code(err) {
	case codes.Canceled, codes.DeadlineExceeded, codes.Unavailable, codes.Internal:
		return err
	}
	return status.Error(codes.PermissionDenied, status.Convert(err).Message())
}

// HostSessionServer serves codefly.dev/session over fixture authentication.
type HostSessionServer struct {
	sessionv1.UnimplementedSessionServiceServer
	service *business.Service
}

// NewHostSessionServer adapts fixture sign-in.
func NewHostSessionServer(service *business.Service) *HostSessionServer {
	return &HostSessionServer{service: service}
}

// SignIn opens a session for a fixture person through the fixture provider.
func (s *HostSessionServer) SignIn(ctx context.Context, req *sessionv1.SignInRequest) (*sessionv1.Session, error) {
	// The provider is the fixture person's own, as the fixture file declares it;
	// naming one here would only be checked against it.
	out, err := s.service.Authenticate(ctx, &gen.AuthenticateRequest{
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{Token: req.GetFixtureToken()}},
	})
	if err != nil {
		return nil, interfaceStatus(err)
	}
	// The session id is the access token's; the interface promises a session
	// id a person-owned Issue can name, and Issue compares it to the bearer's.
	claims, err := accessClaimsOf(out.GetAccessToken())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// The bearer a person presents on every call is this host's access token.
	session := &sessionv1.Session{
		SessionId: claims.SessionID, PersonId: out.GetUser().GetUuid(), OrganizationId: claims.OrgID,
		Roles: []string{claims.OrgRole}, Bearer: out.GetAccessToken(),
	}
	// The person signed into the organization the claims name, if any.
	if err := s.nameForDisplay(ctx, session, claims.OrgID != ""); err != nil {
		return nil, err
	}
	if claims.OrgID != "" {
		if err := s.teamsOf(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, s.lifetime(ctx, session)
}

// lifetime states, on session, the two things a caller that keeps anything for
// it reads: when it ends on its own, and the grant epoch of what it holds now.
// A session that is not open (ended, or past that moment) is unauthenticated:
// this is the one read of a session's lifetime, so an ended or expired bearer
// is refused wherever a session is answered.
func (s *HostSessionServer) lifetime(ctx context.Context, session *sessionv1.Session) error {
	endsAt, err := s.service.SessionEndsAt(ctx, session.GetSessionId())
	if errors.Is(err, business.ErrNoOpenSession) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot read when the session ends: %v", err)
	}
	session.ExpiresAtUnix = endsAt.Unix()
	session.GrantEpoch, err = grantEpoch(ctx, session)
	return err
}

// grantEpoch is the grant epoch of who session acts as: a digest of what the
// authority core answers that session holds now, the derivation the local
// authority states. It covers the person as this host reads them (the
// organisation role they administer by, their teams), each installation the
// session holds a role on (through its role view or cast) with its revision,
// its target's delivered generation and the roles held, and each installation
// by composition of a module that declares no roles. So it differs whenever a
// role is granted or revoked to the person, a team of theirs or the team a
// cast acts as, when they leave a team, and when an installation is made,
// upgraded or ended, and is the same while none of that changed. A session
// the core refuses to read (a cast that no longer holds) holds nothing, and
// its epoch says so. It is opaque and compared for equality only.
//
// It is read, not kept: this host has no single counter that moves on every
// one of those changes (a role grant moves no revision), so the epoch is
// derived from the records themselves, in one snapshot.
func grantEpoch(ctx context.Context, session *sessionv1.Session) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "person %q at %q roles %q teams %q cast %q\n", session.GetPersonId(), session.GetOrganizationId(),
		slices.Sorted(slices.Values(session.GetRoles())), slices.Sorted(slices.Values(session.GetTeamIds())), session.GetCastTeamId())
	digest := func() string { return fmt.Sprintf("sha256:%x", h.Sum(nil)) }
	core, err := authorityCore()
	if err != nil || session.GetOrganizationId() == "" {
		// No core, or a session in no organisation: it holds nothing on any
		// installation, and its epoch is who it is.
		return digest(), nil
	}
	caller := authority.Person{ID: session.GetPersonId(), Organization: session.GetOrganizationId(), SessionID: session.GetSessionId()}
	err = oneRead(ctx, func(ctx context.Context) error {
		generation := func(target string) uint64 {
			m, _ := core.Composed(ctx, target)
			return m.Generation
		}
		held, err := core.HeldInstallations(ctx, caller, caller.Organization)
		if _, refused := domain.CodeOf(err); refused {
			fmt.Fprintf(h, "refused\n")
			return nil
		} else if err != nil {
			return err
		}
		for _, i := range held {
			fmt.Fprintf(h, "held %q revision %d generation %d roles %q\n", i.Installation.ID, i.Installation.Revision, generation(i.Installation.Target), i.Roles)
		}
		composed, err := core.ComposedInstallations(ctx, caller, caller.Organization)
		if _, refused := domain.CodeOf(err); err != nil && !refused {
			return err
		}
		for _, i := range composed {
			fmt.Fprintf(h, "composed %q revision %d generation %d\n", i.ID, i.Revision, generation(i.Target))
		}
		return nil
	})
	if err != nil {
		return "", status.Errorf(codes.Unavailable, "cannot read what the session holds to state its grant epoch: %v", err)
	}
	return digest(), nil
}

// End ends the session the caller's bearer belongs to and every role view and
// cast opened from it: each of those bearers is unauthenticated from then on,
// wherever a session is read. It is a sign-out, and the way back from a cast,
// whose parent session is untouched. Only an interactive session of a person
// calls (signedInPerson), as OpenRoleView.
func (s *HostSessionServer) End(ctx context.Context, _ *sessionv1.EndRequest) (*sessionv1.EndResponse, error) {
	person, err := signedInPerson(ctx, "end a session")
	if err != nil {
		return nil, err
	}
	err = s.service.EndSession(ctx, person.ID, person.Organization, person.SessionID)
	switch {
	case errors.Is(err, business.ErrNoOpenSession):
		return nil, status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, business.ErrSessionEndIncomplete):
		return nil, status.Error(codes.Unavailable, err.Error())
	case err != nil:
		return nil, status.Errorf(codes.Unavailable, "the session could not be ended: %v", err)
	}
	return &sessionv1.EndResponse{}, nil
}

// teamsOf names, on session, the teams its person belongs to in its
// organization, as SignIn and Current state them.
func (s *HostSessionServer) teamsOf(ctx context.Context, session *sessionv1.Session) error {
	teams, err := s.service.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: session.OrganizationId, MemberId: session.PersonId})
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	for _, team := range teams.GetTeams() {
		session.TeamIds = append(session.TeamIds, team.GetId())
	}
	return nil
}

// nameForDisplay fills the session's display names from the person's own
// record and, for a member of the organization the session names, the
// organization's; a session in no organization, or whose person left it,
// names none.
func (s *HostSessionServer) nameForDisplay(ctx context.Context, session *sessionv1.Session, member bool) error {
	org := session.GetOrganizationId()
	if !member {
		org = ""
	}
	person, organization, err := s.service.SessionDisplayNames(ctx, session.GetPersonId(), org)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot read the session's display names: %v", err)
	}
	session.PersonDisplayName, session.OrganizationDisplayName = person, organization
	return nil
}

// Current answers the session the caller's bearer belongs to, as the perimeter
// verified it (signature, window and revocation): the person the request is
// for, their organisation, their role and teams there. The bearer is not
// handed back. A credential that names no session (an API key, a Work
// Context) is not a session.
func (s *HostSessionServer) Current(ctx context.Context, _ *sessionv1.CurrentRequest) (*sessionv1.Session, error) {
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok || identity.SessionID == uuid.Nil || identity.EffectiveSubject == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "the call carries no session bearer")
	}
	session := &sessionv1.Session{SessionId: identity.SessionID.String(), PersonId: identity.EffectiveSubjectID()}
	if identity.OrgID == uuid.Nil {
		if err := s.nameForDisplay(ctx, session, false); err != nil {
			return nil, err
		}
		return session, s.lifetime(ctx, session)
	}
	session.OrganizationId = identity.OrgID.String()
	return session, s.describe(ctx, session)
}

// OpenRoleView opens a session that acts as some of the roles the caller's
// own session acts as on one installation. The authority core decides and
// records the view under the new session's id; this host then mints that
// session, access-only, from the session the person called with. A view is
// never widened here or anywhere: the session it was opened from is the way
// back. Only an interactive session of a person calls (signedInPerson).
func (s *HostSessionServer) OpenRoleView(ctx context.Context, req *sessionv1.OpenRoleViewRequest) (*sessionv1.Session, error) {
	person, err := signedInPerson(ctx, "open a role view")
	if err != nil {
		return nil, err
	}
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	minter := s.service.JWTMinter()
	if minter == nil {
		return nil, status.Error(codes.Unavailable, "this host mints no sessions")
	}
	userID, err := uuid.Parse(person.ID)
	parent, perr := uuid.Parse(person.SessionID)
	if err != nil || perr != nil {
		return nil, status.Error(codes.PermissionDenied, "a role view is opened from a session of a person")
	}
	viewID := business.NewID()
	if _, err := core.OpenRoleView(ctx, person, authority.RoleViewRequest{
		Organization: person.Organization, InstallationID: req.GetInstallationId(), SessionID: viewID.String(), Roles: req.GetRoles()}); err != nil {
		return nil, coreStatus(err)
	}
	pair, err := minter.MintRoleView(ctx, userID, parent, viewID)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "the role view could not be opened as a session: %v", err)
	}
	session := &sessionv1.Session{SessionId: viewID.String(), PersonId: person.ID, OrganizationId: person.Organization, Bearer: pair.AccessToken}
	return session, s.describe(ctx, session)
}

// OpenTeamCast opens a session that acts as a member of one team and of
// nothing else. Whether the caller may is the authority core's decision, by
// its one rule (a person acts as a team whose roles they could grant); this
// host asks, and on a yes mints the session the core recorded the cast under,
// exactly as it mints a role view: access-only, holding no platform role, from
// the session the person called with, which stays the way back. A refusal is
// the core's, handed back with its reason.
func (s *HostSessionServer) OpenTeamCast(ctx context.Context, req *sessionv1.OpenTeamCastRequest) (*sessionv1.Session, error) {
	person, err := signedInPerson(ctx, "act as a team")
	if err != nil {
		return nil, err
	}
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	minter := s.service.JWTMinter()
	if minter == nil {
		return nil, status.Error(codes.Unavailable, "this host mints no sessions")
	}
	userID, err := uuid.Parse(person.ID)
	parent, perr := uuid.Parse(person.SessionID)
	if err != nil || perr != nil {
		return nil, status.Error(codes.PermissionDenied, "a cast is opened from a session of a person")
	}
	castID := business.NewID()
	if _, err := core.OpenTeamCast(ctx, person, authority.TeamCastRequest{
		Organization: person.Organization, Team: req.GetTeamId(), SessionID: castID.String()}); err != nil {
		return nil, coreStatus(err)
	}
	pair, err := minter.MintRoleView(ctx, userID, parent, castID)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "the cast could not be opened as a session: %v", err)
	}
	session := &sessionv1.Session{SessionId: castID.String(), PersonId: person.ID, OrganizationId: person.Organization, Bearer: pair.AccessToken}
	return session, s.describe(ctx, session)
}

// ListCastableTeams answers the teams the caller may act as now, each with
// what it holds and named as this host names its teams. Which teams is the
// authority core's answer, by OpenTeamCast's own rule; this host adds only the
// names. The callers OpenTeamCast refuses are refused here.
func (s *HostSessionServer) ListCastableTeams(ctx context.Context, _ *sessionv1.ListCastableTeamsRequest) (*sessionv1.ListCastableTeamsResponse, error) {
	person, err := signedInPerson(ctx, "list the teams to act as")
	if err != nil {
		return nil, err
	}
	core, err := authorityCore()
	if err != nil {
		return nil, err
	}
	teams, err := core.CastableTeams(ctx, person, person.Organization)
	if err != nil {
		return nil, coreStatus(err)
	}
	out := &sessionv1.ListCastableTeamsResponse{}
	if len(teams) == 0 {
		return out, nil
	}
	named, err := s.service.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: person.Organization})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "cannot read the organization's team names: %v", err)
	}
	names := map[string]string{}
	for _, team := range named.GetTeams() {
		names[team.GetId()] = team.GetName()
	}
	for _, team := range teams {
		listed := &sessionv1.CastableTeam{TeamId: team.Team, DisplayName: cmp.Or(names[team.Team], team.Team)}
		for _, h := range team.Holds {
			listed.Holds = append(listed.Holds, &sessionv1.TeamHolding{InstallationId: h.InstallationID, Target: h.Target, Role: h.Role})
		}
		out.Teams = append(out.Teams, listed)
	}
	return out, nil
}

// describe names, on session, what Current and OpenRoleView answer of the
// organisation it is in: the person's role, display names, teams, the role
// view it was opened with, when it ends on its own and its grant epoch.
func (s *HostSessionServer) describe(ctx context.Context, session *sessionv1.Session) error {
	role, err := lookupMembership(ctx, session.OrganizationId, session.PersonId)
	if err != nil {
		return membershipLookupStatus("cannot read the session's membership", err)
	}
	if role != "" {
		// The role as SignIn and the fixture listing state it.
		session.Roles = []string{business.OrgRoleName(gen.OrgRole(gen.OrgRole_value[role]))}
	}
	// The organization is named for display to a member only: a session whose
	// person left it does not name it to them.
	if err := s.nameForDisplay(ctx, session, role != ""); err != nil {
		return err
	}
	if err := s.teamsOf(ctx, session); err != nil {
		return err
	}
	if err := s.roleViewOf(ctx, session); err != nil {
		return err
	}
	return s.lifetime(ctx, session)
}

// roleViewOf names, on session, the role view or the cast it was opened with.
func (s *HostSessionServer) roleViewOf(ctx context.Context, session *sessionv1.Session) error {
	core, err := authorityCore()
	if err != nil {
		return nil // no core, no role views to state
	}
	view, err := core.RoleViewOf(ctx, session.SessionId)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot read the session's role view: %v", err)
	}
	for _, id := range slices.Sorted(maps.Keys(view.Roles)) {
		session.RoleView = append(session.RoleView, &sessionv1.RoleViewEntry{InstallationId: id, Roles: view.Roles[id]})
	}
	if view.Team != "" {
		// A cast is that team and nothing else: it holds no organization role
		// and belongs to no other team, whatever its person does.
		session.CastTeamId, session.Roles, session.TeamIds = view.Team, nil, []string{view.Team}
	}
	return nil
}

// ListFixturePeople answers who fixture sign-in admits, in the fixture's order;
// none where fixture sign-in is not enabled, which is every deployment that
// signs real people in.
func (s *HostSessionServer) ListFixturePeople(ctx context.Context, _ *sessionv1.ListFixturePeopleRequest) (*sessionv1.ListFixturePeopleResponse, error) {
	people, err := s.service.ListFixturePeople(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &sessionv1.ListFixturePeopleResponse{}
	for _, p := range people {
		out.People = append(out.People, &sessionv1.FixturePerson{
			PersonId: p.PersonID, Email: p.Email, OrganizationId: p.OrganizationID,
			Roles: p.Roles, TeamIds: p.TeamIDs, FixtureToken: p.FixtureToken,
			OrganizationDisplayName: p.OrganizationDisplayName,
		})
	}
	return out, nil
}

// --- conversions ---------------------------------------------------------------

// accessClaims are the fields of this host's own access token the session
// interface reports back. The token was issued a moment ago by this process, so
// its payload is read, not re-verified; nothing is decided on it.
type accessClaims struct {
	SessionID string `json:"sid"`
	OrgID     string `json:"org"`
	OrgRole   string `json:"or"`
}

func accessClaimsOf(token string) (accessClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return accessClaims{}, errors.New("access token is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return accessClaims{}, err
	}
	var claims accessClaims
	return claims, json.Unmarshal(raw, &claims)
}

// interfaceStatus maps a session refusal to a status naming its reason.
func interfaceStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	if errors.Is(err, accountsauth.ErrUnknownIdentity) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, fmt.Sprintf("%v", err))
}
