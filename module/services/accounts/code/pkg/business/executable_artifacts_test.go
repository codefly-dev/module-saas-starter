package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func artifactTestPolicy() ExecutableArtifactPolicy {
	return ExecutableArtifactPolicy{Schema: "example.artifact/v1", Sources: map[string]string{"acme.example": "acme/example"}, Activate: ArtifactPermission{"definitions", "configure"}, Run: ArtifactPermission{"definitions", "run"}, Contracts: []ArtifactContract{{"model", "example/chat", "sha256:" + strings.Repeat("a", 64)}, {"policy", "example/bounded", "sha256:" + strings.Repeat("b", 64)}}, RequiredKinds: []string{"model", "policy"}}
}
func artifactTestRequest() ExecutableArtifactRequest {
	p := artifactTestPolicy()
	return ExecutableArtifactRequest{Installation: uuid.NewString(), Policy: "definitions", Identity: ExecutableArtifactIdentity{Schema: p.Schema, Source: "acme.example", Subject: []byte(`{"bindings":[],"scripts":[],"files":[],"revision":9007199254740993}`), Contracts: p.Contracts, ExpectedRevision: 9007199254740993}}
}
func TestExecutableArtifactIdentityAndPolicy(t *testing.T) {
	p := artifactTestPolicy()
	req := artifactTestRequest()
	caller := ModuleCaller{PrincipalID: uuid.NewString(), BoundOrg: uuid.NewString()}
	require.NoError(t, validateArtifactPolicies(map[string]ExecutableArtifactPolicy{"definitions": p}))
	b, digest, err := artifactEnvelope(caller, caller.BoundOrg, req, p)
	require.NoError(t, err)
	require.Contains(t, string(b), `"expected_revision":9007199254740993`)
	_, same, err := artifactEnvelope(caller, caller.BoundOrg, req, p)
	require.NoError(t, err)
	require.Equal(t, digest, same)
	for _, mutate := range []func(*ExecutableArtifactRequest){func(r *ExecutableArtifactRequest) {
		r.Identity.Subject = append(append([]byte{}, r.Identity.Subject...), '\n')
	}, func(r *ExecutableArtifactRequest) { r.Identity.ExpectedRevision++ }, func(r *ExecutableArtifactRequest) { r.Installation = uuid.NewString() }, func(r *ExecutableArtifactRequest) {
		r.Identity.Contracts = append([]ArtifactContract{}, r.Identity.Contracts...)
		r.Identity.Contracts[0], r.Identity.Contracts[1] = r.Identity.Contracts[1], r.Identity.Contracts[0]
	}} {
		changed := req
		mutate(&changed)
		_, got, err := artifactEnvelope(caller, caller.BoundOrg, changed, p)
		require.NoError(t, err)
		require.NotEqual(t, digest, got)
	}
	for _, mutate := range []func(*ExecutableArtifactRequest){func(r *ExecutableArtifactRequest) { r.Identity.Schema = "foreign/v1" }, func(r *ExecutableArtifactRequest) { r.Identity.Source = "foreign" }, func(r *ExecutableArtifactRequest) { r.Identity.Contracts = nil }, func(r *ExecutableArtifactRequest) {
		r.Identity.Contracts = []ArtifactContract{{"model", "other", p.Contracts[0].Digest}, p.Contracts[1]}
	}} {
		changed := req
		mutate(&changed)
		_, _, err := artifactEnvelope(caller, caller.BoundOrg, changed, p)
		require.Error(t, err)
	}
	p.Run.Action = "execute"
	_, changed, err := artifactEnvelope(caller, caller.BoundOrg, req, p)
	require.NoError(t, err)
	require.NotEqual(t, digest, changed, "policy changes invalidate approval")
	raw, _ := json.Marshal(map[string]any{"example": map[string]any{"tenant": caller.BoundOrg, "artifact_policies": map[string]ExecutableArtifactPolicy{"definitions": p}}})
	registry, err := ParseModulePrincipalRegistry(string(raw))
	require.NoError(t, err)
	require.Equal(t, p, registry[ModulePrincipalID("example")].ArtifactPolicies["definitions"])
	p.Contracts[0].Digest = "*"
	require.Error(t, validateArtifactPolicies(map[string]ExecutableArtifactPolicy{"definitions": p}))
}

type artifactDecisionStore struct {
	Store
	InstallationStore
	rows         map[string]*ExecutableArtifactApproval
	installation *gen.Installation
	member       bool
	admin        bool
	permission   bool
	puts         int
	tenant       string
}

func (s *artifactDecisionStore) WithOrgTx(ctx context.Context, tenant string, fn func(context.Context) error) error {
	s.tenant = tenant
	return fn(ctx)
}
func (s *artifactDecisionStore) GetInstallation(context.Context, string, string) (*gen.Installation, gen.InstallationHealth, error) {
	return s.installation, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, nil
}
func (s *artifactDecisionStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	if !s.member {
		return nil, nil
	}
	role := gen.OrgRole_ORG_ROLE_MEMBER
	if s.admin {
		role = gen.OrgRole_ORG_ROLE_ADMIN
	}
	return &gen.OrgMembership{Role: role}, nil
}
func (s *artifactDecisionStore) CheckPermission(context.Context, string, gen.SubjectKind, string, string, string, string) (bool, string, error) {
	return s.permission, "", nil
}
func (s *artifactDecisionStore) PutExecutableArtifactApproval(_ context.Context, a *ExecutableArtifactApproval) (*ExecutableArtifactApproval, bool, error) {
	if old := s.rows[a.Digest]; old != nil {
		return old, false, nil
	}
	s.rows[a.Digest] = a
	s.puts++
	return a, true, nil
}
func (s *artifactDecisionStore) GetExecutableArtifactApproval(_ context.Context, _, _, _, digest string) (*ExecutableArtifactApproval, error) {
	return s.rows[digest], nil
}
func (s *artifactDecisionStore) RevokeExecutableArtifactApproval(_ context.Context, _, _, _, digest, _ string) (bool, error) {
	a := s.rows[digest]
	if a.RevokedAt != nil {
		return false, nil
	}
	now := time.Now()
	a.RevokedAt = &now
	return true, nil
}
func TestExecutableArtifactConsentNeverInferredFromRun(t *testing.T) {
	req := artifactTestRequest()
	tenant := uuid.NewString()
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("example"), BoundOrg: tenant}
	store := &artifactDecisionStore{rows: map[string]*ExecutableArtifactApproval{}, member: true, admin: true, permission: true, installation: &gen.Installation{Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, SolutionIdentifier: "acme/example"}}
	s, err := NewService(store)
	require.NoError(t, err)
	s.modulePrincipals = ModulePrincipalRegistry{caller.PrincipalID: {Prefix: "example", Tenant: tenant, ArtifactPolicies: map[string]ExecutableArtifactPolicy{"definitions": artifactTestPolicy()}}}
	decide := func(action string) (*ExecutableArtifactApproval, error) {
		return s.DecideExecutableArtifact(context.Background(), caller, tenant, uuid.NewString(), "example", action, req)
	}
	_, err = decide("authorize")
	require.ErrorContains(t, err, "exact artifact approval")
	require.Zero(t, store.puts)
	store.admin = false
	_, err = decide("approve")
	require.ErrorContains(t, err, "artifact authority")
	require.Zero(t, store.puts)
	store.admin = true
	approved, err := decide("approve")
	require.NoError(t, err)
	again, err := decide("approve")
	require.NoError(t, err)
	require.Equal(t, approved.ID, again.ID)
	require.Equal(t, 1, store.puts)
	_, err = decide("authorize")
	require.NoError(t, err)
	require.Equal(t, tenant, store.tenant)
	store.permission = false
	_, err = decide("authorize")
	require.Error(t, err)
	store.permission = true
	store.installation.SolutionIdentifier = "foreign"
	_, err = decide("authorize")
	require.Error(t, err)
	store.installation.SolutionIdentifier = "acme/example"
	store.admin = false
	_, err = decide("authorize")
	require.NoError(t, err, "tenant consent survives approver departure; current run authority still required")
	store.admin = true
	_, _, err = s.RevokeApprovedExecutableArtifact(context.Background(), caller, tenant, uuid.NewString(), "example", req.Installation, approved.ID, func(ArtifactPermission) error { return nil })
	require.NoError(t, err)
	_, err = decide("authorize")
	require.ErrorContains(t, err, "revoked")
	_, err = decide("approve")
	require.ErrorContains(t, err, "revoked")
	require.Equal(t, 1, store.puts)
}

func (s *artifactDecisionStore) GetExecutableArtifactApprovalByID(_ context.Context, _, _, _, id string) (*ExecutableArtifactApproval, error) {
	for _, row := range s.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return nil, nil
}

func (s *artifactDecisionStore) CheckExecutableArtifactAuthority(_ context.Context, _, _, _ string, _ ArtifactPermission, admin bool) (bool, error) {
	return s.member && (!admin || s.admin) && s.permission, nil
}

type artifactDecisionStoreScope struct {
	Scoped
	store *artifactDecisionStore
	id    Identity
}

func (s *artifactDecisionStore) As(id Identity) Scoped {
	return &artifactDecisionStoreScope{store: s, id: id}
}
func (s *artifactDecisionStoreScope) Within(ctx context.Context, fn func(context.Context) error) error {
	return s.store.WithOrgTx(ctx, s.id.OrgID, fn)
}
