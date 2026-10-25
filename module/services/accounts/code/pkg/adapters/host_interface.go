package adapters

// The host authority interfaces (the authority interface repository), served by accounts as
// adapters over what it already does. Nothing here decides authority: every
// operation maps onto an existing path, and where no path exists the refusal
// names the gap instead of inventing one. The same four interfaces are served by
// the local authority for a local run, and the authority interface repository's pkg/conformance is
// the suite both must pass.

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// HostAuthorityServer serves codefly.dev/authority over the Work Context authority.
type HostAuthorityServer struct {
	authorityv1.UnimplementedAuthorityServiceServer
	workContexts *WorkContextAuthorityServer
	modules      *ModuleCapabilitiesServer
	// roles answers solution roles; nil reads the service.
	roles solutionRoleReader
	// methods holds a call to a delivered method policy; nil reads the service.
	methods methodPolicyChecker
}

// methodPolicyChecker holds a call's scopes to what the audience module's
// applied delivery declares for one method.
type methodPolicyChecker interface {
	CheckDeliveredMethod(ctx context.Context, audience, method string, held []*basev0.WorkScopeV1) error
}

func (s *HostAuthorityServer) methodPolicies() methodPolicyChecker {
	if s.methods != nil {
		return s.methods
	}
	if service == nil {
		return nil
	}
	return service
}

// NewHostAuthorityServer adapts the Work Context authority.
func NewHostAuthorityServer(workContexts *WorkContextAuthorityServer, modules *ModuleCapabilitiesServer) *HostAuthorityServer {
	return &HostAuthorityServer{workContexts: workContexts, modules: modules}
}

// Keys is the issuer and the one Work Context signing key.
func (s *HostAuthorityServer) Keys(context.Context, *authorityv1.KeysRequest) (*authorityv1.KeysResponse, error) {
	if !s.workContexts.configured() {
		return nil, status.Error(codes.FailedPrecondition, ErrWorkContextAuthorityUnconfigured.Error())
	}
	return &authorityv1.KeysResponse{Issuer: s.workContexts.issuer, Keys: []*authorityv1.PublicKey{{
		KeyId: s.workContexts.keyID, Algorithm: workcontext.Algorithm, PublicKey: []byte(s.workContexts.publicKey),
	}}}, nil
}

// Check verifies for the audience the caller is: core's full check with the
// host's live seals and revisions, exactly as a module capability is verified.
func (s *HostAuthorityServer) Check(ctx context.Context, req *authorityv1.CheckRequest) (*authorityv1.CheckResponse, error) {
	if req.GetAudience() == "" {
		return nil, status.Error(codes.InvalidArgument, "a check names the audience the caller is")
	}
	verified, err := s.workContexts.verifyFor(ctx, req.GetToken(), req.GetAudience())
	if err != nil {
		return nil, interfaceStatus(err)
	}
	// The capability is sound. A named method is then held to what the
	// audience module's delivery declares for it, against what the call's
	// current actor holds (the last hop's grant on a delegated capability, never
	// the owner's authority above it); advisory reports that decision beside
	// the capability instead of refusing.
	refusal := ""
	refusalCode := codes.PermissionDenied
	if method := req.GetMethod(); method != "" {
		if policies := s.methodPolicies(); policies == nil {
			refusal = methodPolicyRefusal(req.GetAudience(), method)
		} else if err := policies.CheckDeliveredMethod(ctx, req.GetAudience(), method, effectiveScopes(verified.Context())); err != nil {
			refusal = err.Error()
			if errors.Is(err, business.ErrDeliveredMethodUnknown) {
				refusalCode = codes.NotFound
			}
		}
		if refusal != "" && !req.GetAdvisory() {
			return nil, status.Error(refusalCode, refusal)
		}
	}
	wire, err := workContextToWire(verified.Context())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &authorityv1.CheckResponse{Context: wire, AuthorizationRevision: verified.Context().GetAuthorizationRevision(), Refusal: refusal}, nil
}

// methodPolicyRefusal names the fail-closed answer when no service is wired.
func methodPolicyRefusal(audience, method string) string {
	return fmt.Sprintf("%s's delivery declares no method policy, so %s is refused", audience, method)
}

// Issue maps each owner shape onto the mint that already exists for it. Each
// shape is authenticated first (the bearer, the workload credential, the actor
// credential), and only then is its audience held to the composition, so a
// caller proving nothing learns nothing of which modules are composed.
func (s *HostAuthorityServer) Issue(ctx context.Context, req *authorityv1.IssueRequest) (*authorityv1.IssueResponse, error) {
	if _, ok := req.GetAs().(*authorityv1.IssueRequest_Delegation); ok {
		return nil, status.Error(codes.Unimplemented, "an issue from a delegation: "+delegationGap)
	}
	// An installation proves its solution's workload before anything it asks
	// is read, so its audience is required there, once the caller is proven.
	if _, installation := req.GetAs().(*authorityv1.IssueRequest_Installation); req.GetAudience() == "" && !installation {
		derived, err := bindingAudience(ctx, req)
		if err != nil {
			return nil, err
		}
		req = proto.CloneOf(req)
		req.Audience = derived
	}
	ttl, err := issueTTL(req)
	if err != nil {
		return nil, err
	}
	scopes := scopesToHost(req.GetScopes())
	var issued *gen.IssuedWorkContext
	switch as := req.GetAs().(type) {
	case *authorityv1.IssueRequest_Person:
		// The person is whoever the perimeter verified from the bearer on this
		// call; the request names nobody, by the interface's design.
		identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "a person-owned issue is made by the signed-in person")
		}
		if err := requireComposed(req.GetAudience()); err != nil {
			return nil, err
		}
		issued, err = s.personIssue(ctx, identity, req, scopes, ttl)
		if err != nil {
			return nil, interfaceStatus(err)
		}
	case *authorityv1.IssueRequest_Installation:
		issued, err = s.forInstallation(ctx, as.Installation.GetInstallationId(), req, scopes, ttl)
	case *authorityv1.IssueRequest_ParentToken:
		if req.GetActor() == nil {
			issued, err = s.exchangeOrRenew(ctx, as.ParentToken, req, scopes, ttl)
			break
		}
		issued, err = s.childHop(ctx, as.ParentToken, req)
	case *authorityv1.IssueRequest_Workload:
		issued, err = s.workloadIssue(ctx, as.Workload.GetCredential(), req, ttl)
	default:
		return nil, status.Error(codes.InvalidArgument, "an issue names whom it is for: a person, a workload, an installation, a parent token or a delegation")
	}
	if err != nil {
		return nil, interfaceStatus(err)
	}
	decoded, err := workcontext.Decode(issued.GetToken())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	wire, err := workContextToWire(decoded.Context())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &authorityv1.IssueResponse{Token: issued.GetToken(), ExpiresAtUnix: issued.GetExpiresAt().GetSeconds(), Context: wire}, nil
}

// requireComposed refuses, NotFound by name, an audience the composition does
// not declare: by the contract it does not exist, which is not the host's own
// "not one this host serves" denial. It runs only once the caller's shape is
// authenticated.
func requireComposed(audience string) error {
	if service == nil || !service.IsDeclaredModulePrincipal(business.ModulePrincipalID(audience)) {
		return status.Errorf(codes.NotFound, "audience %q is not composed", audience)
	}
	return nil
}

// issueTTL is the lifetime an issue asks for, clamped to the issuer's ceiling
// as the interface states; zero leaves it to the mint.
func issueTTL(req *authorityv1.IssueRequest) (int32, error) {
	ceiling := int64(workcontext.DefaultMaxTTL / time.Second)
	switch seconds := req.GetTtlSeconds(); {
	case seconds < 0:
		return 0, status.Errorf(codes.InvalidArgument, "a lifetime is not negative: %d seconds", seconds)
	case seconds > ceiling:
		return int32(ceiling), nil
	default:
		return int32(seconds), nil
	}
}

// personIssue is a signed-in person's own capability at the audience.
func (s *HostAuthorityServer) personIssue(ctx context.Context, identity accountsauth.RequestIdentity, req *authorityv1.IssueRequest, scopes []*gen.WorkContextScope, ttl int32) (*gen.IssuedWorkContext, error) {
	if err := requireOrgMember(ctx, identity.EffectiveSubject.String(), identity.OrgID.String()); err != nil {
		return nil, err
	}
	// An audience the organisation never installed has no installation to seal
	// a capability to, whatever would decide what the person holds there: that
	// is the failed precondition, named, before a solution's roles read an
	// installation that is not there and answer that the person holds nothing.
	if _, err := s.workContexts.audienceInstallation(ctx, identity.OrgID.String(), req.GetAudience()); err != nil {
		return nil, err
	}
	// A solution that declares roles decides what the person holds there.
	authority, held, err := s.solutionRoles().PersonSolutionRoles(ctx, identity.OrgID.String(), identity.EffectiveSubject.String(), req.GetAudience())
	if err != nil {
		return nil, interfaceStatus(err)
	}
	if authority != nil {
		if binding := req.GetOperationBindingId(); binding != "" {
			if operation := req.GetOperation(); operation != "" && operation != "invoke" {
				return nil, status.Errorf(codes.InvalidArgument, "a person-owned issue carries invoke scopes for operation binding %q; operation %q is refused", binding, operation)
			}
			return s.solutionBindingIssue(ctx, identity.OrgID.String(), req.GetAudience(), binding, authority, held, scopes, ttl)
		}
		return s.solutionPersonIssue(ctx, identity.OrgID.String(), req.GetAudience(), authority, held, scopes, ttl)
	}
	var bindings []string
	if binding := req.GetOperationBindingId(); binding != "" {
		if operation := req.GetOperation(); operation != "" && operation != "invoke" {
			return nil, status.Errorf(codes.InvalidArgument, "a person-owned issue carries invoke scopes for operation binding %q; operation %q is refused", binding, operation)
		}
		bindings = []string{binding}
	}
	if len(scopes) == 0 && len(bindings) == 0 {
		// An empty ask is everything the person holds at the audience.
		if scopes, err = s.workContexts.personHoldings(ctx, identity.OrgID.String(), req.GetAudience()); err != nil {
			return nil, interfaceStatus(err)
		}
	}
	// A fresh task per issue: the interface names no task, the host requires
	// one as the identity tuple's third element.
	return s.workContexts.StartTask(ctx, &gen.StartTaskWorkContextRequest{
		OrgId: identity.OrgID.String(), TaskId: uuid.NewString(), Audience: req.GetAudience(), AuthorityScopes: scopes, TtlSeconds: ttl,
		OperationBindings: bindings,
	})
}

// exchangeOrRenew is one more issue from a capability, with no actor: the
// bearer of whoever holds it authorizes it. At another audience it is the
// host's audience exchange. At the parent's own audience it is a renewal, a
// fresh window on the same task: a delegated capability's current actor renews
// it (RenewWorkContext, which may narrow it), and an owner's own capability is
// re-issued as another root session of its task (StartRootSession), which
// keeps its authority as it stands and so takes no scopes. The organisation is
// the request's, or the parent's own when it names none. An empty ask is the
// parent's own authority: what its current actor holds. The parent's claims are
// only read to route the issue; each path verifies it.
func (s *HostAuthorityServer) exchangeOrRenew(ctx context.Context, parentToken string, req *authorityv1.IssueRequest, scopes []*gen.WorkContextScope, ttl int32) (*gen.IssuedWorkContext, error) {
	if _, ok := accountsauth.VerifiedRequestIdentity(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "an exchange or a renewal is made by the capability's owner, signed in; the call carries no bearer")
	}
	if err := requireComposed(req.GetAudience()); err != nil {
		return nil, err
	}
	decoded, err := workcontext.Decode(parentToken)
	if err != nil {
		return nil, interfaceStatus(err)
	}
	parent := decoded.Context()
	org := cmp.Or(req.GetOrganizationId(), parent.GetTenantId())
	if req.GetAudience() == parent.GetAudience() {
		if len(parent.GetActorChain()) > 0 {
			return s.workContexts.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
				OrgId: org, ParentWorkContextToken: parentToken, AttenuatedScopes: scopes, TtlSeconds: ttl,
			})
		}
		if len(scopes) > 0 {
			return nil, status.Errorf(codes.InvalidArgument, "a renewal of an owner's own capability at %s keeps its authority as it stands and names no scopes; an exchange narrows it", req.GetAudience())
		}
		// The request's session id is validated and never read: the issuer
		// assigns the new session (StartRootSession).
		return s.workContexts.StartRootSession(ctx, &gen.StartRootSessionWorkContextRequest{
			OrgId: org, ParentWorkContextToken: parentToken, SessionId: uuid.NewString(), Audience: req.GetAudience(), TtlSeconds: ttl,
		})
	}
	if len(scopes) == 0 {
		scopes = hostScopesOf(effectiveScopes(parent))
	}
	return s.workContexts.ExchangeAudience(ctx, &gen.ExchangeWorkContextAudienceRequest{
		OrgId: org, ParentWorkContextToken: parentToken, Audience: req.GetAudience(), AttenuatedScopes: scopes, TtlSeconds: ttl,
	})
}

// credentialRefusal names why a workload credential did not prove a module:
// the host could not establish what the caller runs (Unavailable: nothing was
// decided), no credential, the token review refused it, or the workload it
// names is not declared by exactly one principal.
func credentialRefusal(err error, which string) error {
	switch {
	case errors.Is(err, business.ErrExecutionUnbound):
		return status.Errorf(codes.Unavailable, "%s credential: %v", which, err)
	case errors.Is(err, business.ErrExecutionNotBoundToken):
		return status.Errorf(codes.Unauthenticated, "%s credential refused: %v", which, err)
	case errors.Is(err, business.ErrExecutionTokenRejected):
		return status.Errorf(codes.PermissionDenied, "%s credential refused: %v", which, business.ErrExecutionTokenRejected)
	case errors.Is(err, business.ErrExecutionWorkloadAmbiguous):
		return status.Errorf(codes.PermissionDenied, "%s credential refused: %v", which, err)
	}
	if mapped := moduleIdentityError(err, codes.PermissionDenied, which+" credential refused"); mapped != nil {
		return mapped
	}
	return status.Errorf(codes.PermissionDenied, "%s credential refused: %v", which, err)
}

// bindingAudience is the audience of an issue that names none: an issue
// through a binding the acting module declares is addressed to that binding's
// audience, as the interface's callers send it (a hop's ForModule as the
// workload, its ForChild with the workload as actor). Any other shape names
// its audience.
func bindingAudience(ctx context.Context, req *authorityv1.IssueRequest) (string, error) {
	var credential string
	switch as := req.GetAs().(type) {
	case *authorityv1.IssueRequest_Workload:
		credential = as.Workload.GetCredential()
	case *authorityv1.IssueRequest_ParentToken:
		credential = req.GetActor().GetCredential()
	}
	binding := req.GetOperationBindingId()
	if credential == "" || binding == "" || service == nil {
		return "", status.Error(codes.InvalidArgument, "an issue names its audience")
	}
	prefix, err := service.ModulePrefixOfWorkload(ctx, credential)
	if err != nil {
		return "", credentialRefusal(err, "workload")
	}
	declared, ok := service.ModulePrincipals()[business.ModulePrincipalID(prefix)].OperationAudiences[binding]
	if !ok || declared.Audience == "" {
		return "", status.Errorf(codes.NotFound, "operation binding %q is not declared by %s", binding, prefix)
	}
	return declared.Audience, nil
}

// Delegate, RevokeDelegation, GetDelegation: GAP. The host's standing grants
// are source delegations (DelegationService) bound to a source; a person's
// grant to a module by scopes is not yet behind the interface.
func (s *HostAuthorityServer) Delegate(context.Context, *authorityv1.DelegateRequest) (*authorityv1.Delegation, error) {
	return nil, status.Error(codes.Unimplemented, delegationGap)
}

// RevokeDelegation: GAP, see Delegate.
func (s *HostAuthorityServer) RevokeDelegation(context.Context, *authorityv1.RevokeDelegationRequest) (*authorityv1.RevokeDelegationResponse, error) {
	return nil, status.Error(codes.Unimplemented, delegationGap)
}

// GetDelegation: GAP, see Delegate.
func (s *HostAuthorityServer) GetDelegation(context.Context, *authorityv1.GetDelegationRequest) (*authorityv1.Delegation, error) {
	return nil, status.Error(codes.Unimplemented, delegationGap)
}

const delegationGap = "delegation to a module: accounts holds source delegations bound to a source; a grant by scopes is not yet behind the interface"

// HostCompositionServer serves codefly.dev/composition.
type HostCompositionServer struct {
	compositionv1.UnimplementedCompositionServiceServer
}

// ApplyManifest authenticates the signed carrier at the delivery owner.
func (s *HostCompositionServer) ApplyManifest(ctx context.Context, req *compositionv1.ApplyManifestRequest) (*compositionv1.ApplyManifestResponse, error) {
	if service == nil {
		return nil, status.Error(codes.Unavailable, "composition service is not wired")
	}
	out, err := service.ApplyComposition(ctx, req)
	if err == nil {
		return out, nil
	}
	httpStatus, reason := solutionDeliveryStatus(err)
	code := codes.Unavailable
	switch httpStatus {
	case http.StatusBadRequest:
		code = codes.InvalidArgument
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
		code = codes.PermissionDenied
	case http.StatusConflict:
		code = codes.FailedPrecondition
	}
	return nil, status.Error(code, reason)
}

// HostInstallationServer serves codefly.dev/installation over the installation service.
type HostInstallationServer struct {
	installationv1.UnimplementedInstallationServiceServer
	installations *InstallationServer
	service       *business.Service
}

// NewHostInstallationServer adapts the installation service.
func NewHostInstallationServer(installations *InstallationServer, service *business.Service) *HostInstallationServer {
	return &HostInstallationServer{installations: installations, service: service}
}

// List answers what one organisation has installed: an administrator's read,
// over the host's organisation-installation listing.
func (s *HostInstallationServer) List(ctx context.Context, req *installationv1.ListRequest) (*installationv1.ListResponse, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	out, err := s.installations.ListOrganizationInstallations(ctx, &gen.ListOrganizationInstallationsRequest{OrgId: req.GetOrganizationId()})
	if err != nil {
		return nil, err
	}
	resp := &installationv1.ListResponse{}
	for _, row := range out.GetInstallations() {
		details, err := s.service.ReadHostInstallation(ctx, req.GetOrganizationId(), row.GetInstallation().GetId())
		if err != nil {
			return nil, mapInstallationError(err)
		}
		resp.Installations = append(resp.Installations, hostInstallationToWire(details))
	}
	return resp, nil
}

// Install names the target and the role by name; the host's resolver
// (InstallTargetByName) reads them from the organisation's catalogue and role
// catalogue and installs through the host's own install path. A target the
// organisation already holds is refused naming the installation it holds.
func (s *HostInstallationServer) Install(ctx context.Context, req *installationv1.InstallRequest) (*installationv1.Installation, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	installation, installed, err := s.service.InstallTargetByName(ctx, actorID, req.GetOrganizationId(), req.GetTarget(), req.GetRole(), req.GetOwnerPersonId())
	switch {
	case errors.Is(err, business.ErrInstallTargetNotPresent):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case err != nil:
		return nil, mapInstallationError(err)
	case !installed:
		return nil, status.Errorf(codes.PermissionDenied, "%q is already installed in organization %s, as installation %s", req.GetTarget(), req.GetOrganizationId(), installation.GetId())
	}
	details, err := s.service.ReadHostInstallation(ctx, req.GetOrganizationId(), installation.GetId())
	if err != nil {
		return nil, mapInstallationError(err)
	}
	out := hostInstallationToWire(details)
	out.Target = req.GetTarget()
	return out, nil
}

// GrantToTeam resolves the installation's standing role through the grant owner.
func (s *HostInstallationServer) GrantToTeam(ctx context.Context, req *installationv1.GrantToTeamRequest) (*installationv1.Installation, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	// Only an active installation is exposed: one still pending its
	// acknowledgement, or revoked, is refused naming which it is.
	current, err := s.service.ReadHostInstallation(ctx, req.GetOrganizationId(), req.GetInstallationId())
	if err != nil {
		return nil, mapInstallationError(err)
	}
	if st := current.Installation.GetStatus(); st != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
		return nil, status.Errorf(codes.PermissionDenied, "installation %s is %s; only an active installation can be exposed", req.GetInstallationId(), installationStatusWord(st))
	}
	details, err := s.service.ExposeHostInstallation(ctx, actorID, req.GetOrganizationId(), req.GetInstallationId(), req.GetTeamId())
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return hostInstallationToWire(details), nil
}

// installationStatusWord is an installation's status as a refusal names it.
func installationStatusWord(st gen.InstallationStatus) string {
	switch st {
	case gen.InstallationStatus_INSTALLATION_STATUS_PENDING:
		return "pending"
	case gen.InstallationStatus_INSTALLATION_STATUS_REVOKED:
		return "revoked"
	case gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE:
		return "active"
	}
	return "not active"
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
	return session, nil
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
		return session, nil
	}
	session.OrganizationId = identity.OrgID.String()
	role, err := lookupMembership(ctx, session.OrganizationId, session.PersonId)
	if err != nil {
		return nil, membershipLookupStatus("cannot read the session's membership", err)
	}
	if role != "" {
		// The role as SignIn and the fixture listing state it.
		session.Roles = []string{business.OrgRoleName(gen.OrgRole(gen.OrgRole_value[role]))}
	}
	// The organization is named for display to a member only: a session whose
	// person left it does not name it to them.
	if err := s.nameForDisplay(ctx, session, role != ""); err != nil {
		return nil, err
	}
	teams, err := s.service.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: session.OrganizationId, MemberId: session.PersonId})
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	for _, team := range teams.GetTeams() {
		session.TeamIds = append(session.TeamIds, team.GetId())
	}
	return session, nil
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

// hostScopesOf is core scopes in the host's own request shape.
func hostScopesOf(in []*basev0.WorkScopeV1) []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(in))
	for _, sc := range in {
		out = append(out, &gen.WorkContextScope{ResourceKind: sc.GetResourceKind(), Actions: sc.GetActions(), ResourceIds: sc.GetResourceIds()})
	}
	return out
}

func scopesToHost(in []*authorityv1.WorkScopeV1) []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(in))
	for _, sc := range in {
		out = append(out, &gen.WorkContextScope{ResourceKind: sc.GetResourceKind(), Actions: sc.GetActions(), ResourceIds: sc.GetResourceIds()})
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

// installationToWire is an installation as the interface states it. Its target
// is the name the composition gives what is installed (the presence's route
// alias, "notes"), the name Install takes and a frontend binds to, never this
// host's row id for the presence; a row whose presence is gone keeps the id.
func installationToWire(in *gen.Installation, target string) *installationv1.Installation {
	st := installationv1.Installation_STATUS_UNSPECIFIED
	switch in.GetStatus() {
	case gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE:
		st = installationv1.Installation_STATUS_ACTIVE
	case gen.InstallationStatus_INSTALLATION_STATUS_PENDING:
		st = installationv1.Installation_STATUS_PENDING
	case gen.InstallationStatus_INSTALLATION_STATUS_REVOKED:
		st = installationv1.Installation_STATUS_REVOKED
	}
	return &installationv1.Installation{
		Id: in.GetId(), OrganizationId: in.GetOrgId(), Target: cmp.Or(target, in.GetTargetId()), Status: st,
		AgentPrincipalId: in.GetAgentPrincipalId(),
	}
}

// interfaceStatus maps a refusal to a status naming its reason, as the suite
// requires: every refusal reaches the caller.
func interfaceStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, accountsauth.ErrUnknownIdentity):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, workcontext.ErrRevoked), errors.Is(err, workcontext.ErrNoSeal):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, workcontext.ErrInvalid), errors.Is(err, workcontext.ErrNotACoreToken), errors.Is(err, workcontext.ErrReplayed):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, workcontext.ErrNoBinding):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, fmt.Sprintf("%v", err))
}

// childHop derives a module's hop on a context addressed to it behind the
// interface: the parent token is the authentication, the actor's credential
// names the module, and the hop names the binding it exercises (bindingHop:
// the owner's current grants through that binding). There is one rule for a
// module hop: a hop naming no binding is refused, as is a parent addressed to
// another module, both before the parent is verified, so a refused hop spends
// no single-use parent.
func (s *HostAuthorityServer) childHop(ctx context.Context, parentToken string, req *authorityv1.IssueRequest) (*gen.IssuedWorkContext, error) {
	prefix, err := service.ModulePrefixOfWorkload(ctx, req.GetActor().GetCredential())
	if err != nil {
		return nil, credentialRefusal(err, "actor")
	}
	if err := requireComposed(req.GetAudience()); err != nil {
		return nil, err
	}
	if req.GetOperationBindingId() == "" {
		return nil, status.Errorf(codes.PermissionDenied, "a module's hop names the binding it exercises; %s named none", prefix)
	}
	// The parent's claims are only read to refuse it; nothing is issued on them.
	if decoded, derr := workcontext.Decode(parentToken); derr == nil && decoded.Context().GetAudience() != prefix {
		return nil, status.Errorf(codes.PermissionDenied, "%s hops only on a token addressed to it; this one is addressed to %s", prefix, decoded.Context().GetAudience())
	}
	if err := requireDeclaredHopBinding(prefix, req); err != nil {
		return nil, err
	}
	// The audience guards every mint runs, before the parent is spent.
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	parent, err := s.workContexts.verifyFor(ctx, parentToken, prefix)
	if err != nil {
		return nil, interfaceStatus(err)
	}
	return s.bindingHop(ctx, prefix, parent, req)
}

func hostInstallationToWire(details *business.HostInstallationDetails) *installationv1.Installation {
	out := installationToWire(details.Installation, details.Target)
	out.Role, out.Revision, out.TeamIds = details.Role, details.Revision, details.TeamIDs
	return out
}
