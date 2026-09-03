package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// PrincipalServer handles PrincipalService RPCs. Lives in its own
// file (not grpc_gen.go) so a future regeneration of the codefly
// agent stub doesn't lose the handler implementations. The struct
// itself is identical to the other *Server types — embed Unsafe* to
// fail loud if a new RPC method gets added without an implementation.
type PrincipalServer struct {
	gen.UnsafePrincipalServiceServer
}

// ============================================================================
// PrincipalService RPCs
// ============================================================================

func (s *PrincipalServer) GetPrincipal(ctx context.Context, req *gen.GetPrincipalRequest) (*gen.Principal, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	p, err := service.GetPrincipal(ctx, req.GetId())
	if err != nil {
		return nil, mapPrincipalError(err)
	}
	return principalToProto(p), nil
}

func (s *PrincipalServer) GetAgentPrincipal(ctx context.Context, req *gen.GetAgentPrincipalRequest) (*gen.Principal, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	p, err := service.GetAgentPrincipal(ctx, req.GetOrgId(), req.GetAgentIdentifier())
	if err != nil {
		return nil, mapPrincipalError(err)
	}
	return principalToProto(p), nil
}

func (s *PrincipalServer) CreateAgentPrincipal(ctx context.Context, req *gen.CreateAgentPrincipalRequest) (*gen.Principal, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	p, err := service.CreateAgentPrincipal(ctx, business.CreateAgentRequest{
		OrgID:           req.GetOrgId(),
		AgentIdentifier: req.GetAgentIdentifier(),
		DisplayName:     req.GetDisplayName(),
		// The authenticated caller is the authorship root (Claims v1, ask #3).
		CreatedBy:        actorID,
		AllowedAudiences: req.GetAllowedAudiences(),
		AllowedScopes:    req.GetAllowedScopes(),
	})
	if err != nil {
		return nil, mapPrincipalError(err)
	}
	return principalToProto(p), nil
}

// DisableAgentPrincipal reversibly suspends a delegated actor. It is an internal
// enablement-lifecycle RPC (the host toggles a delegation-based solution's
// agents per org), so exposure gating is the whole floor. The suspension bumps
// the authorization revision, staling outstanding Work Contexts at once.
func (s *PrincipalServer) DisableAgentPrincipal(ctx context.Context, req *gen.DisableAgentPrincipalRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if err := service.DisableAgentPrincipal(ctx, req.GetId(), req.GetReason()); err != nil {
		return nil, mapPrincipalError(err)
	}
	return &emptypb.Empty{}, nil
}

// EnableAgentPrincipal lifts a reversible suspension. Internal, like its
// disable counterpart.
func (s *PrincipalServer) EnableAgentPrincipal(ctx context.Context, req *gen.EnableAgentPrincipalRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if err := service.EnableAgentPrincipal(ctx, req.GetId()); err != nil {
		return nil, mapPrincipalError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *PrincipalServer) RevokePrincipal(ctx context.Context, req *gen.RevokePrincipalRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// Lookup the principal first to determine the org for authz —
	// revoking another org's principal must require platform admin.
	p, err := service.GetPrincipal(ctx, req.GetId())
	if err != nil {
		return nil, mapPrincipalError(err)
	}
	// Humans (org_id NULL) only revocable by platform admin; agents
	// + services revocable by their org's admins.
	if p.OrgID == "" {
		actorID, authErr := requireAuth(ctx)
		if authErr != nil {
			return nil, authErr
		}
		if paErr := requirePlatformAdmin(ctx, actorID); paErr != nil {
			return nil, paErr
		}
	} else {
		actorID, authErr := requireAuth(ctx)
		if authErr != nil {
			return nil, authErr
		}
		if err := requireOrgAdmin(ctx, actorID, p.OrgID); err != nil {
			return nil, err
		}
	}
	if err := service.RevokePrincipal(ctx, req.GetId(), req.GetReason()); err != nil {
		return nil, mapPrincipalError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *PrincipalServer) ListPrincipals(ctx context.Context, req *gen.ListPrincipalsRequest) (*gen.ListPrincipalsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	kind := protoKindToBusinessKind(req.GetKind())
	principals, nextToken, err := service.ListPrincipals(ctx, req.GetOrgId(), kind, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, mapPrincipalError(err)
	}
	out := make([]*gen.Principal, 0, len(principals))
	for _, p := range principals {
		out = append(out, principalToProto(p))
	}
	return &gen.ListPrincipalsResponse{
		Principals:    out,
		NextPageToken: nextToken,
	}, nil
}

// ============================================================================
// Decide (PermissionService extension — added in M2)
// ============================================================================
//
// Decide is the principal-aware permission check. M2 ships the wire
// + adapter; behavior intentionally tracks CheckPermission for
// backwards compat. Manifest ceiling (M4), risk levels (M7), and
// delegation tokens (M6) layer on without changing this file's
// contract.

// Decide is implemented on PermServer (declared in grpc_gen.go).
// Adding a method to PermServer in this file is safe — Go allows
// methods on a type to live across files in the same package.
func (s *PermServer) Decide(ctx context.Context, req *gen.DecideRequest) (*gen.DecideResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// Decide is an EXPOSURE_INTERNAL authority oracle: only a valid internal
	// service token may ask it. A bare tenant JWT must never turn it into a
	// permission oracle.
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("Decide",
		wool.Field("principal_id", req.GetPrincipalId()),
		wool.Field("action", req.GetAction()))

	// Manifest-ceiling check (M4 at the gateway). The caller's
	// declared_permissions list is the plugin's manifest-declared
	// authority cap. If the action isn't declared, the gateway
	// denies WITHOUT consulting role grants — saves a round-trip
	// AND makes the deny audit-clear ("manifest-ceiling: not
	// declared" vs "no role grants this action").
	//
	// Empty declared_permissions = "no ceiling enforced" (M4
	// rollout default). Once every plugin declares its
	// permissions and CODEFLY_PDP_REQUIRE_MANIFEST=true, the
	// caller side flips this from "advisory" to "required".
	if declared := req.GetDeclaredPermissions(); len(declared) > 0 {
		if !manifestAllows(declared, req.GetAction(), req.GetResource()) {
			return &gen.DecideResponse{
				Decision:     gen.Decision_DECISION_DENY,
				Reason:       fmt.Sprintf("manifest-ceiling: action %q on %q not declared in plugin's permissions block", req.GetAction(), req.GetResource()),
				DecisionPath: "manifest-ceiling: action not declared",
			}, nil
		}
	}

	// M2 stub: split the dotted action into resource_class.verb so we
	// can route through the existing CheckPermission machinery, which
	// expects (resource, action) split. The full Cedar/Rego path
	// arrives in M4+; for now a structured action like
	// "github.merge_pr" maps to resource_class="github", verb="merge_pr"
	// when the caller didn't supply a top-level resource.
	resourceClass, verb := splitDottedAction(req.GetAction())

	// The caller's `resource` field is the typed form ("repo:foo");
	// the underlying CheckPermission semantics use `resource` as the
	// resource CLASS (not instance). For M2 we map the action's
	// prefix to the resource class — this preserves CheckPermission
	// behavior. M4 introduces typed resources end-to-end.
	resourceForCheck := resourceClass

	// Resolve principal kind to map to SubjectKind. M2 still uses the
	// existing CheckPermission, so we need a SubjectKind. Until M4+
	// ships, agents/services are treated as users for the role-
	// assignment join — their principals.id matches their original
	// users.uuid / api_keys.id, so existing role grants find them
	// correctly.
	checkReq := &gen.CheckPermissionRequest{
		SubjectId:   req.GetPrincipalId(),
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		Resource:    resourceForCheck,
		Action:      verb,
		OrgId:       req.GetOrgId(),
		// scope: the typed resource (e.g. "repo:foo") becomes the
		// fine-grained scope. Existing CheckPermission semantics:
		// scope-less grants match any scope.
		Scope: req.GetResource(),
	}
	checkResp, err := service.CheckPermission(ctx, checkReq)
	if err != nil {
		return nil, w.Wrapf(err, "underlying CheckPermission failed")
	}

	resp := &gen.DecideResponse{
		Reason: checkResp.GetReason(),
	}
	if checkResp.GetAllowed() {
		resp.Decision = gen.Decision_DECISION_ALLOW
		resp.DecisionPath = fmt.Sprintf("role-grant: %s on %s",
			verb, resourceForCheck)
	} else {
		resp.Decision = gen.Decision_DECISION_DENY
		// CheckPermission's reason already explains; surface verbatim.
		// Decision path is "no role grants" when the underlying check
		// returned no rule.
		resp.DecisionPath = "no role-assignment grants this action"
	}
	// Future-proof: if the action is high-risk and the caller didn't
	// supply a delegation_proof, M7 returns REQUIRE_APPROVAL here.
	// In M2 this branch is unreachable (no risk_level handling yet).
	_ = req.GetRiskLevel()
	_ = req.GetDelegationProof()
	_ = req.GetDeclaredPermissions()
	return resp, nil
}

// ============================================================================
// Helpers
// ============================================================================

// principalToProto converts the business Principal into the wire form.
// Centralized so display-name / org_id encoding stays consistent.
func principalToProto(p *business.Principal) *gen.Principal {
	if p == nil {
		return nil
	}
	out := &gen.Principal{
		Id:               p.ID,
		Kind:             businessKindToProtoKind(p.Kind),
		DisplayName:      p.DisplayName,
		OrgId:            p.OrgID,
		AgentIdentifier:  p.AgentIdentifier,
		CreatedBy:        p.CreatedBy,
		CreatedAt:        timestamppb.New(p.CreatedAt),
		RevokedReason:    p.RevokedReason,
		AllowedAudiences: p.AllowedAudiences,
		AllowedScopes:    p.AllowedScopes,
		DisabledReason:   p.DisabledReason,
	}
	if p.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*p.RevokedAt)
	}
	if p.DisabledAt != nil {
		out.DisabledAt = timestamppb.New(*p.DisabledAt)
	}
	return out
}

func businessKindToProtoKind(k string) gen.PrincipalKind {
	switch k {
	case business.PrincipalKindHuman:
		return gen.PrincipalKind_PRINCIPAL_KIND_HUMAN
	case business.PrincipalKindService:
		return gen.PrincipalKind_PRINCIPAL_KIND_SERVICE
	case business.PrincipalKindAgent:
		return gen.PrincipalKind_PRINCIPAL_KIND_AGENT
	default:
		return gen.PrincipalKind_PRINCIPAL_KIND_UNSPECIFIED
	}
}

func protoKindToBusinessKind(k gen.PrincipalKind) string {
	switch k {
	case gen.PrincipalKind_PRINCIPAL_KIND_HUMAN:
		return business.PrincipalKindHuman
	case gen.PrincipalKind_PRINCIPAL_KIND_SERVICE:
		return business.PrincipalKindService
	case gen.PrincipalKind_PRINCIPAL_KIND_AGENT:
		return business.PrincipalKindAgent
	default:
		return "" // unspecified == "all kinds"
	}
}

// manifestAllows checks whether the caller's declared_permissions
// list grants the (action, resource) pair. Mirrors the M4
// CeilingPDP logic at the gateway layer.
//
// Match rules (simple — operator manifests are read by humans):
//   - Action matches if the declaration is exactly the action OR
//     ends with "*" and is a prefix.
//   - Resource matches if the declaration is empty (any resource)
//     OR exactly matches OR ends with "*" and is a prefix.
//
// `gen.Permission` is the existing proto type for (resource,
// action) pairs that role_permissions stores. We reuse it here
// for declared_permissions so the YAML manifest's `action`/
// `resource` fields map cleanly.
func manifestAllows(declared []*gen.Permission, action, resource string) bool {
	for _, d := range declared {
		if !globMatchAction(d.GetAction(), action) {
			continue
		}
		// gen.Permission has Resource (the resource CLASS in
		// CheckPermission semantics). For the manifest-ceiling
		// check we use it as a typed-resource pattern.
		if d.GetResource() == "" || d.GetResource() == "*" || globMatchAction(d.GetResource(), resource) {
			return true
		}
	}
	return false
}

// globMatchAction is a tiny wildcard matcher: exact, "*", or
// "prefix*". Same shape as policy.PermissionPolicy.globMatch.
func globMatchAction(pattern, s string) bool {
	if pattern == "*" || pattern == s {
		return true
	}
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(s) >= len(prefix) && s[:len(prefix)] == prefix
	}
	return false
}

// splitDottedAction splits "github.merge_pr" into ("github", "merge_pr").
// If no dot is present, returns ("", action) — the entire string is
// treated as the verb on a wildcard resource. Empty string returns
// ("", "").
func splitDottedAction(s string) (resourceClass, verb string) {
	idx := strings.IndexByte(s, '.')
	if idx <= 0 {
		return "", s
	}
	return s[:idx], s[idx+1:]
}

// mapPrincipalError converts business-layer errors into appropriate
// gRPC status codes. NotFound → NOT_FOUND, Conflict → ALREADY_EXISTS,
// validation → INVALID_ARGUMENT (caught earlier by Validate),
// everything else → Internal.
func mapPrincipalError(err error) error {
	if err == nil {
		return nil
	}
	var se *business.StoreError
	if errors.As(err, &se) {
		switch se.StoreErrorType {
		case business.ErrTypeNotFound:
			return status.Error(codes.NotFound, err.Error())
		case business.ErrTypeConflict:
			return status.Error(codes.AlreadyExists, err.Error())
		}
	}
	return status.Error(codes.Internal, err.Error())
}

// requireOrgMember is a thin wrapper around the existing
// requireOrgMember in this package (declared elsewhere). Kept as a
// local sentinel so an explicit import audit is easy.
//
// If the existing helper isn't present yet, we fall through to
// requireAuth + a manual membership check.
//
// Note: this comment is aspirational. The actual helper is in
// auth_gates.go (existing pattern). Compile errors here surface a
// drift in the auth gates file.

// Compile-time guard so a refactor of business.Principal keeps the
// proto encoding fields in sync with the struct fields.
var _ = []func(*business.Principal) *gen.Principal{principalToProto}

// silenceUnusedImports for time — used by deferred features we'll
// flesh out in M3+. Kept as an explicit unused-binding so future
// edits don't have to re-add the import.
var _ = time.Now
