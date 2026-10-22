package adapters

// The host authority interfaces (the authority interface repository), served by accounts as
// adapters over what it already does. Nothing here decides authority: every
// operation maps onto an existing path, and where no path exists the refusal
// names the gap instead of inventing one. The same four interfaces are served by
// the mini gateway for a local run, and the authority interface repository's pkg/conformance is
// the suite both must pass.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
}

// NewHostAuthorityServer adapts the Work Context authority.
func NewHostAuthorityServer(workContexts *WorkContextAuthorityServer) *HostAuthorityServer {
	return &HostAuthorityServer{workContexts: workContexts}
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
	wire, err := workContextToWire(verified.Context())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &authorityv1.CheckResponse{Context: wire, AuthorizationRevision: verified.Context().GetAuthorizationRevision()}, nil
}

// Issue maps each owner shape onto the mint that already exists for it.
func (s *HostAuthorityServer) Issue(ctx context.Context, req *authorityv1.IssueRequest) (*authorityv1.IssueResponse, error) {
	if req.GetAudience() == "" {
		return nil, status.Error(codes.InvalidArgument, "an issue names its audience")
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
		// A fresh task per issue: the interface names no task, the host requires
		// one as the identity tuple's third element.
		issued, err = s.workContexts.StartTask(ctx, &gen.StartTaskWorkContextRequest{
			OrgId: identity.OrgID.String(), TaskId: uuid.NewString(), Audience: req.GetAudience(), AuthorityScopes: scopes, TtlSeconds: ttl,
		})
	case *authorityv1.IssueRequest_Installation:
		// The installation is the one the caller's service credential names; the
		// host resolves it from the credential, so the id is checked after the mint.
		issued, err = s.workContexts.StartInstallationTask(ctx, &gen.StartInstallationTaskRequest{
			Audience: req.GetAudience(), AuthorityScopes: scopes, TtlSeconds: ttl,
		})
	case *authorityv1.IssueRequest_ParentToken:
		if req.GetActor() != nil {
			issued, err = s.workContexts.StartChildSession(ctx, &gen.StartChildSessionWorkContextRequest{
				ParentWorkContextToken: as.ParentToken, Audience: req.GetAudience(), GrantedScopes: scopes, TtlSeconds: ttl,
			})
		} else {
			issued, err = s.workContexts.ExchangeAudience(ctx, &gen.ExchangeWorkContextAudienceRequest{
				ParentWorkContextToken: as.ParentToken, Audience: req.GetAudience(), AttenuatedScopes: scopes, TtlSeconds: ttl,
			})
		}
	case *authorityv1.IssueRequest_Workload:
		// GAP (rung 1): the host mints a module's own capability on the gateway's
		// workload endpoint (/modules/_operation-context) from a pod token and an
		// installed binding, not on an accounts RPC. Serving it here needs the
		// broker's attestation moved behind this method.
		return nil, status.Error(codes.Unimplemented, "workload-owned issue: accounts mints this on the gateway's workload endpoint today; not yet behind the interface")
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

// HostCompositionServer serves codefly.dev/composition.
type HostCompositionServer struct {
	compositionv1.UnimplementedCompositionServiceServer
}

// ApplyManifest: GAP (rung 1). The host learns a module from a signed
// ModuleManifest delivered to /platform/_delivery/manifest and reconciles it
// through the solution host bindings; the interface's ModuleManifestV1 is the
// proto form of that document. Serving it here means the delivery path accepts
// the proto manifest, with the ceiling read from it rather than from the envelope.
func (s *HostCompositionServer) ApplyManifest(context.Context, *compositionv1.ApplyManifestRequest) (*compositionv1.ApplyManifestResponse, error) {
	return nil, status.Error(codes.Unimplemented, "apply manifest: accounts learns a module through signed delivery at /platform/_delivery/manifest today; not yet behind the interface")
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
		resp.Installations = append(resp.Installations, installationToWire(row.GetInstallation()))
	}
	return resp, nil
}

// Install: GAP (rung 1). The host installs by immutable target id, agent
// identifier and role id; the interface names the target and the role by name.
// Serving it here needs a resolver from the applied presence (alias → target id,
// agent identifier) and from the role catalogue (name → id).
func (s *HostInstallationServer) Install(ctx context.Context, req *installationv1.InstallRequest) (*installationv1.Installation, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	return nil, status.Error(codes.Unimplemented, "install by target name and role name: accounts installs by target id, agent identifier and role id; the resolver is not yet behind the interface")
}

// GrantToTeam: GAP (rung 1), same resolver: the host grants a role id at the
// installation's root; the interface grants the installation's own role.
func (s *HostInstallationServer) GrantToTeam(ctx context.Context, req *installationv1.GrantToTeamRequest) (*installationv1.Installation, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrganizationId()); err != nil {
		return nil, err
	}
	return nil, status.Error(codes.Unimplemented, "grant to team: accounts grants a role id at the root; the installation's own role is not yet resolved behind the interface")
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

func installationToWire(in *gen.Installation) *installationv1.Installation {
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
		Id: in.GetId(), OrganizationId: in.GetOrgId(), Target: in.GetTargetId(), Status: st,
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
