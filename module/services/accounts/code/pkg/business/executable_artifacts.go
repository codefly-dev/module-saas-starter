package business

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExecutableArtifactPolicy is an operator-installed ceiling, not supplied by
// an author. Source identifiers are mapped explicitly to installation identities;
// event namespaces are never treated as executable-source ownership.
type ExecutableArtifactPolicy struct {
	Schema        string             `json:"schema"`
	Sources       map[string]string  `json:"sources"`
	Activate      ArtifactPermission `json:"activate"`
	Run           ArtifactPermission `json:"run"`
	Contracts     []ArtifactContract `json:"contracts"`
	RequiredKinds []string           `json:"required_kinds"`
}
type ArtifactPermission struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}
type ArtifactContract struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// ExecutableArtifactIdentity is the complete consuming module's exported
// identity, never authored bytes. Subject is an exact, versioned canonical byte
// encoding owned by Schema; the host does not reinterpret another module's
// compiler hash. Ordered contracts include every executable dependency, checked
// against the installed policy before an approval can be created or read.
type ExecutableArtifactIdentity struct {
	Schema           string             `json:"schema"`
	Source           string             `json:"source"`
	Subject          []byte             `json:"subject"`
	Contracts        []ArtifactContract `json:"contracts"`
	ExpectedRevision int64              `json:"expected_revision"`
}
type ExecutableArtifactRequest struct {
	Installation string                     `json:"installation"`
	Policy       string                     `json:"policy"`
	Identity     ExecutableArtifactIdentity `json:"identity"`
}
type ExecutableArtifactApproval struct {
	ID           string
	OrgID        string
	Installation string
	Module       string
	Policy       string
	Digest       string
	Subject      []byte
	ApprovedBy   string
	CreatedAt    time.Time
	RevokedAt    *time.Time
}
type ExecutableArtifactStore interface {
	CheckExecutableArtifactAuthority(context.Context, string, string, string, ArtifactPermission, bool) (bool, error)
	// Insert is immutable and idempotent by exact digest. A revoked row is never
	// resurrected. The result reports whether this transaction inserted the row.
	PutExecutableArtifactApproval(context.Context, *ExecutableArtifactApproval) (*ExecutableArtifactApproval, bool, error)
	GetExecutableArtifactApproval(context.Context, string, string, string, string) (*ExecutableArtifactApproval, error)
	GetExecutableArtifactApprovalByID(context.Context, string, string, string, string) (*ExecutableArtifactApproval, error)
	RevokeExecutableArtifactApproval(context.Context, string, string, string, string, string) (bool, error)
}

var artifactDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func artifactValue(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= 32 || r == 127 {
			return false
		}
	}
	return !strings.Contains(s, "*")
}
func (p *ExecutableArtifactPolicy) UnmarshalJSON(raw []byte) error {
	type plain ExecutableArtifactPolicy
	var out plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return err
	}
	*p = ExecutableArtifactPolicy(out)
	return nil
}
func validateArtifactPolicies(policies map[string]ExecutableArtifactPolicy) error {
	if len(policies) > 32 {
		return errors.New("too many executable artifact policies")
	}
	for id, p := range policies {
		if !artifactValue(id, 128) || !artifactValue(p.Schema, 128) || len(p.Sources) == 0 || len(p.Sources) > 128 || len(p.Contracts) == 0 || len(p.Contracts) > 256 || len(p.RequiredKinds) == 0 {
			return fmt.Errorf("invalid executable artifact policy %q", id)
		}
		for source, installed := range p.Sources {
			if !artifactValue(source, 512) || !artifactValue(installed, 512) {
				return fmt.Errorf("invalid artifact source in %q", id)
			}
		}
		for _, permission := range []ArtifactPermission{p.Activate, p.Run} {
			if !artifactValue(permission.Resource, 128) || !artifactValue(permission.Action, 64) {
				return fmt.Errorf("invalid artifact permission in %q", id)
			}
		}
		seen := map[ArtifactContract]bool{}
		kinds := map[string]bool{}
		for _, c := range p.Contracts {
			if !artifactValue(c.Kind, 64) || !artifactValue(c.Name, 512) || !artifactDigestPattern.MatchString(c.Digest) || seen[c] {
				return fmt.Errorf("invalid artifact contract in %q", id)
			}
			seen[c] = true
			kinds[c.Kind] = true
		}
		required := map[string]bool{}
		for _, kind := range p.RequiredKinds {
			if !kinds[kind] || required[kind] {
				return fmt.Errorf("invalid required artifact kind in %q", id)
			}
			required[kind] = true
		}
	}
	return nil
}
func (s *Service) ExecutableArtifactPolicyFor(caller ModuleCaller, tenant, audience, id string) (ExecutableArtifactPolicy, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return ExecutableArtifactPolicy{}, err
	}
	if err = authorizeTenant(caller, grant, tenant); err != nil {
		return ExecutableArtifactPolicy{}, err
	}
	policy, ok := grant.ArtifactPolicies[id]
	if !ok || grant.Prefix == "" || audience != grant.Prefix {
		return ExecutableArtifactPolicy{}, status.Error(codes.PermissionDenied, "installed artifact policy required")
	}
	// Also applies to in-process configuration; no bypass via a directly-built map.
	if err = validateArtifactPolicies(map[string]ExecutableArtifactPolicy{id: policy}); err != nil {
		return ExecutableArtifactPolicy{}, status.Error(codes.Unavailable, "invalid installed artifact policy")
	}
	return policy, nil
}

type executableArtifactEnvelope struct {
	Version string                    `json:"version"`
	Tenant  string                    `json:"tenant"`
	Module  string                    `json:"module"`
	Request ExecutableArtifactRequest `json:"request"`
	Policy  ExecutableArtifactPolicy  `json:"policy"`
}

func artifactEnvelope(caller ModuleCaller, tenant string, req ExecutableArtifactRequest, policy ExecutableArtifactPolicy) ([]byte, string, error) {
	identity := req.Identity
	if id, err := uuid.Parse(req.Installation); err != nil || id.String() != req.Installation || identity.Schema != policy.Schema || identity.ExpectedRevision < 0 || len(identity.Subject) == 0 || len(identity.Subject) > 65536 || len(identity.Contracts) > 256 {
		return nil, "", status.Error(codes.InvalidArgument, "invalid executable artifact identity")
	}
	if _, ok := policy.Sources[identity.Source]; !ok {
		return nil, "", status.Error(codes.PermissionDenied, "artifact source is not installed")
	}
	// Byte identity is deliberate: no float64 round trip, arbitrary JSON
	// canonicalizer, or independently recomputed module compiler digest.
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, c := range identity.Contracts {
		key := c.Kind + "\x00" + c.Name
		if seen[key] {
			return nil, "", status.Error(codes.InvalidArgument, "duplicate artifact contract")
		}
		seen[key] = true
		allowed := false
		for _, ceiling := range policy.Contracts {
			if c == ceiling {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, "", status.Error(codes.PermissionDenied, "artifact contract is not installed")
		}
		kinds[c.Kind] = true
	}
	for _, kind := range policy.RequiredKinds {
		if !kinds[kind] {
			return nil, "", status.Error(codes.PermissionDenied, "required artifact contract missing")
		}
	}
	// Policy contents participate, so a changed ceiling cannot silently reuse
	// earlier consent. json.Marshal sorts map keys and preserves array order.
	envelope := executableArtifactEnvelope{"host.executable-artifact/v1", tenant, caller.PrincipalID, req, policy}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) > 524288 {
		return nil, "", status.Error(codes.InvalidArgument, "artifact envelope too large")
	}
	sum := sha256.Sum256(encoded)
	return encoded, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DecideExecutableArtifact is invoked only with identities from the verified
// current parent Work Context. "approve" is explicit current-admin consent;
// "authorize" cannot create or renew an approval. Revocation addresses a retained Ref.
func (s *Service) DecideExecutableArtifact(ctx context.Context, caller ModuleCaller, tenant, actor, audience, action string, req ExecutableArtifactRequest) (*ExecutableArtifactApproval, error) {
	if action != "approve" && action != "authorize" {
		return nil, status.Error(codes.InvalidArgument, "unknown artifact decision")
	}
	policy, err := s.ExecutableArtifactPolicyFor(caller, tenant, audience, req.Policy)
	if err != nil {
		return nil, err
	}
	encoded, digest, err := artifactEnvelope(caller, tenant, req, policy)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ExecutableArtifactStore)
	if !ok {
		return nil, status.Error(codes.Unavailable, "artifact approval store unavailable")
	}
	var out *ExecutableArtifactApproval
	err = s.store.As(Identity{OrgID: tenant, UserID: actor}).Within(ctx, func(ctx context.Context) error {
		installation, health, e := s.installationStore().GetInstallation(ctx, tenant, req.Installation)
		if e != nil {
			return e
		}
		if installation == nil || installation.Status != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE || health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY || installation.SolutionIdentifier != policy.Sources[req.Identity.Source] {
			return status.Error(codes.PermissionDenied, "artifact installation unavailable")
		}
		permission := policy.Run
		if action == "approve" {
			permission = policy.Activate
		}
		allowed, e := store.CheckExecutableArtifactAuthority(ctx, tenant, actor, req.Installation, permission, action == "approve")
		if e != nil {
			return e
		}
		if !allowed {
			return status.Error(codes.PermissionDenied, "current artifact authority required")
		}
		changed := false
		if action == "approve" {
			out, changed, e = store.PutExecutableArtifactApproval(ctx, &ExecutableArtifactApproval{ID: uuid.NewString(), OrgID: tenant, Installation: req.Installation, Module: caller.PrincipalID, Policy: req.Policy, Digest: digest, Subject: encoded, ApprovedBy: actor})
		} else {
			out, e = store.GetExecutableArtifactApproval(ctx, tenant, req.Installation, caller.PrincipalID, digest)
		}
		if e != nil {
			return e
		}
		if out == nil || !bytes.Equal(out.Subject, encoded) {
			return status.Error(codes.PermissionDenied, "exact artifact approval required")
		}

		if out.RevokedAt != nil {
			return status.Error(codes.PermissionDenied, "artifact approval revoked")
		}
		// Consent belongs to the tenant. The approver is immutable attribution,
		// not a standing delegation whose departure invalidates committed policy.
		if changed {
			return s.emitTx(ctx, actor, ActorTypeUser, EventExecutableArtifactDecision, "executable_artifact", out.ID, tenant, map[string]any{"action": action, "installation_id": req.Installation, "policy_id": req.Policy, "subject_digest": digest, "module_principal_id": caller.PrincipalID})
		}
		return nil
	})
	return out, err
}

// RevokeApprovedExecutableArtifact addresses the retained row, so disabling or
// changing a policy/installation cannot make old consent impossible to revoke.
// Scope and permission come from that row's original host policy, never the
// request. checkScope checks the verified parent's attenuated capability.
func (s *Service) RevokeApprovedExecutableArtifact(ctx context.Context, caller ModuleCaller, tenant, actor, audience, installation, id string, checkScope func(ArtifactPermission) error) (*ExecutableArtifactApproval, int64, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, 0, err
	}
	if err = authorizeTenant(caller, grant, tenant); err != nil {
		return nil, 0, err
	}
	if audience != grant.Prefix || grant.Prefix == "" || checkScope == nil {
		return nil, 0, status.Error(codes.PermissionDenied, "artifact caller denied")
	}
	store, ok := s.store.(ExecutableArtifactStore)
	if !ok {
		return nil, 0, status.Error(codes.Unavailable, "artifact approval store unavailable")
	}
	var out *ExecutableArtifactApproval
	var envelope executableArtifactEnvelope
	err = s.store.As(Identity{OrgID: tenant, UserID: actor}).Within(ctx, func(ctx context.Context) error {
		var e error
		out, e = store.GetExecutableArtifactApprovalByID(ctx, tenant, installation, caller.PrincipalID, id)
		if e != nil {
			return e
		}
		if out == nil {
			return status.Error(codes.PermissionDenied, "exact artifact approval required")
		}
		if e = json.Unmarshal(out.Subject, &envelope); e != nil || envelope.Version != "host.executable-artifact/v1" || envelope.Tenant != tenant || envelope.Module != caller.PrincipalID || envelope.Request.Installation != installation {
			return status.Error(codes.FailedPrecondition, "invalid retained artifact approval")
		}
		permission := envelope.Policy.Activate
		if e = checkScope(permission); e != nil {
			return e
		}
		allowed, e := store.CheckExecutableArtifactAuthority(ctx, tenant, actor, installation, permission, true)
		if e != nil {
			return e
		}
		if !allowed {
			return status.Error(codes.PermissionDenied, "current artifact revocation authority required")
		}
		changed, e := store.RevokeExecutableArtifactApproval(ctx, tenant, installation, caller.PrincipalID, out.Digest, actor)
		if e != nil {
			return e
		}
		if !changed {
			return nil
		}
		return s.emitTx(ctx, actor, ActorTypeUser, EventExecutableArtifactDecision, "executable_artifact", out.ID, tenant, map[string]any{"action": "revoke", "installation_id": installation, "policy_id": out.Policy, "subject_digest": out.Digest, "module_principal_id": caller.PrincipalID})
	})
	return out, envelope.Request.Identity.ExpectedRevision, err
}
