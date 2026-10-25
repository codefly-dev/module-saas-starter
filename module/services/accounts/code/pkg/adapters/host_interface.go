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
	// audience module's delivery declares for it; advisory reports that
	// decision beside the capability instead of refusing.
	refusal := ""
	refusalCode := codes.PermissionDenied
	if method := req.GetMethod(); method != "" {
		if service == nil {
			refusal = methodPolicyRefusal(req.GetAudience(), method)
		} else if err := service.CheckDeliveredMethod(ctx, req.GetAudience(), method, verified.Context().GetAuthorityScopes()); err != nil {
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

// Issue maps each owner shape onto the mint that already exists for it.
func (s *HostAuthorityServer) Issue(ctx context.Context, req *authorityv1.IssueRequest) (*authorityv1.IssueResponse, error) {
	if req.GetAudience() == "" {
		derived, err := bindingAudience(ctx, req)
		if err != nil {
			return nil, err
		}
		req = proto.CloneOf(req)
		req.Audience = derived
	}
	// An audience that is not composed does not exist, by the contract: refused
	// NotFound by name before any mint, rather than as the host's own
	// "not one this host serves" denial.
	if service == nil || !service.IsDeclaredModulePrincipal(business.ModulePrincipalID(req.GetAudience())) {
		return nil, status.Errorf(codes.NotFound, "audience %q is not composed", req.GetAudience())
	}
	scopes := scopesToHost(req.GetScopes())
	ttl := int32(req.GetTtlSeconds())
	var issued *gen.IssuedWorkContext
	var err error
	switch as := req.GetAs().(type) {
	case *authorityv1.IssueRequest_Person:
		// The person is whoever the perimeter verified from the bearer on this
		// call; the request names nobody, by the interface's design.
		identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "a person-owned issue is made by the signed-in person")
		}
		_ = as
		if err := requireOrgMember(ctx, identity.EffectiveSubject.String(), identity.OrgID.String()); err != nil {
			return nil, err
		}
		if len(scopes) == 0 {
			// An empty ask is everything the person holds at the audience.
			if scopes, err = s.workContexts.personHoldings(ctx, identity.OrgID.String(), req.GetAudience()); err != nil {
				return nil, interfaceStatus(err)
			}
		}
		// A fresh task per issue: the interface names no task, the host requires
		// one as the identity tuple's third element.
		issued, err = s.workContexts.StartTask(ctx, &gen.StartTaskWorkContextRequest{
			OrgId: identity.OrgID.String(), TaskId: uuid.NewString(), Audience: req.GetAudience(), AuthorityScopes: scopes, TtlSeconds: ttl,
		})
	case *authorityv1.IssueRequest_Installation:
		issued, err = s.forInstallation(ctx, as.Installation.GetInstallationId(), req, scopes)
	case *authorityv1.IssueRequest_ParentToken:
		if req.GetActor() == nil {
			// An audience exchange keeps the chain: the owner's bearer authorizes it.
			issued, err = s.workContexts.ExchangeAudience(ctx, &gen.ExchangeWorkContextAudienceRequest{
				ParentWorkContextToken: as.ParentToken, Audience: req.GetAudience(), AttenuatedScopes: scopes, TtlSeconds: ttl,
			})
			break
		}
		issued, err = s.childHop(ctx, as.ParentToken, req)
	case *authorityv1.IssueRequest_Workload:
		// The credential names the module: reviewed by the execution reviewer,
		// matched to the one principal declaring that workload. With a binding
		// named, the issue is the host's operation context to that installed
		// audience; without one, the module's own context to this surface.
		prefix, perr := service.ModulePrefixOfWorkload(ctx, as.Workload.GetCredential())
		if perr != nil {
			if mapped := moduleIdentityError(perr, codes.PermissionDenied, "workload credential refused"); mapped != nil {
				return nil, mapped
			}
			return nil, status.Error(codes.PermissionDenied, "workload credential refused")
		}
		if binding := req.GetOperationBindingId(); binding != "" {
			grant := service.ModulePrincipals()[business.ModulePrincipalID(prefix)]
			if _, declared := grant.OperationAudiences[binding]; !declared {
				return nil, status.Errorf(codes.NotFound, "operation binding %q is not declared by %s", binding, prefix)
			}
			out, merr := s.modules.MintModuleOperationContext(ctx, &gen.ModuleMintOperationContextRequest{
				Prefix: prefix, ServiceAccountToken: as.Workload.GetCredential(), Binding: binding, OrganizationId: req.GetOrganizationId(),
			})
			if merr != nil {
				return nil, interfaceStatus(merr)
			}
			issued = &gen.IssuedWorkContext{Token: out.GetToken(), ExpiresAt: out.GetExpiresAt()}
		} else {
			if req.GetAudience() != ModuleWorkContextAudience {
				return nil, status.Errorf(codes.InvalidArgument, "a module's own context is addressed to %q, not %q", ModuleWorkContextAudience, req.GetAudience())
			}
			out, merr := s.modules.MintModuleWorkContext(ctx, &gen.ModuleMintWorkContextRequest{
				Prefix: prefix, ServiceAccountToken: as.Workload.GetCredential(), OrganizationId: req.GetOrganizationId(),
			})
			if merr != nil {
				return nil, interfaceStatus(merr)
			}
			issued = &gen.IssuedWorkContext{Token: out.GetToken(), ExpiresAt: out.GetExpiresAt()}
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "an issue names whom it is for")
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
		if mapped := moduleIdentityError(err, codes.PermissionDenied, "workload credential refused"); mapped != nil {
			return "", mapped
		}
		return "", status.Error(codes.PermissionDenied, "workload credential refused")
	}
	declared, ok := service.ModulePrincipals()[business.ModulePrincipalID(prefix)].OperationAudiences[binding]
	if !ok || declared.Audience == "" {
		return "", status.Errorf(codes.NotFound, "operation binding %q is not declared by %s", binding, prefix)
	}
	return declared.Audience, nil
}

// ListResources: GAP. The host answers resource coverage through its own
// per-collection reads (CheckWorkContextRecordAccess,
// ListReadableSourceCollections); a grant-shaped answer behind the interface is
// not built yet.
func (s *HostAuthorityServer) ListResources(context.Context, *authorityv1.ListResourcesRequest) (*authorityv1.ListResourcesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "list resources: accounts answers coverage per collection today; the grant-shaped answer is not yet behind the interface")
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
		return nil, status.Errorf(codes.PermissionDenied, "organization %s already holds installation %s of %q", req.GetOrganizationId(), installation.GetId(), req.GetTarget())
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
	details, err := s.service.ExposeHostInstallation(ctx, actorID, req.GetOrganizationId(), req.GetInstallationId(), req.GetTeamId())
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return hostInstallationToWire(details), nil
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
	return &sessionv1.Session{
		SessionId: claims.SessionID, PersonId: out.GetUser().GetUuid(), OrganizationId: claims.OrgID,
		Roles: []string{claims.OrgRole}, Bearer: out.GetAccessToken(),
	}, nil
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
		if mapped := moduleIdentityError(err, codes.PermissionDenied, "actor credential refused"); mapped != nil {
			return nil, mapped
		}
		return nil, status.Error(codes.PermissionDenied, "actor credential refused")
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
