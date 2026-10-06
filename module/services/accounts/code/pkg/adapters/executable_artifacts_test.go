package adapters

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
	"time"
)

type artifactTransportStore struct {
	business.Store
	business.InstallationStore
	rows   map[string]*business.ExecutableArtifactApproval
	member bool
	admin  bool
}

func (s *artifactTransportStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *artifactTransportStore) GetInstallation(context.Context, string, string) (*gen.Installation, gen.InstallationHealth, error) {
	return &gen.Installation{SolutionIdentifier: "acme/example", Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE}, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, nil
}
func (s *artifactTransportStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	if !s.member {
		return nil, nil
	}
	role := gen.OrgRole_ORG_ROLE_MEMBER
	if s.admin {
		role = gen.OrgRole_ORG_ROLE_ADMIN
	}
	return &gen.OrgMembership{Role: role}, nil
}
func (s *artifactTransportStore) CheckPermission(context.Context, string, gen.SubjectKind, string, string, string, string) (bool, string, error) {
	return true, "", nil
}
func (s *artifactTransportStore) PutExecutableArtifactApproval(_ context.Context, a *business.ExecutableArtifactApproval) (*business.ExecutableArtifactApproval, bool, error) {
	if old := s.rows[a.Digest]; old != nil {
		return old, false, nil
	}
	s.rows[a.Digest] = a
	return a, true, nil
}
func (s *artifactTransportStore) GetExecutableArtifactApproval(_ context.Context, _, _, _, digest string) (*business.ExecutableArtifactApproval, error) {
	return s.rows[digest], nil
}
func (s *artifactTransportStore) RevokeExecutableArtifactApproval(_ context.Context, _, _, _, digest, _ string) (bool, error) {
	a := s.rows[digest]
	if a.RevokedAt != nil {
		return false, nil
	}
	now := time.Now()
	a.RevokedAt = &now
	return true, nil
}
func TestExecutableArtifactServedTransport(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	store := &artifactTransportStore{rows: map[string]*business.ExecutableArtifactApproval{}, member: true, admin: true}
	var err error
	service, err = business.NewService(store)
	require.NoError(t, err)
	digest := "sha256:" + strings.Repeat("a", 64)
	policy := business.ExecutableArtifactPolicy{Schema: "example.artifact/v1", Sources: map[string]string{"acme.example": "acme/example"}, Activate: business.ArtifactPermission{Resource: "definitions", Action: "configure"}, Run: business.ArtifactPermission{Resource: "definitions", Action: "run"}, Contracts: []business.ArtifactContract{{Kind: "model", Name: "example/chat", Digest: digest}}, RequiredKinds: []string{"model"}}
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg, ArtifactPolicies: map[string]business.ExecutableArtifactPolicy{"content": policy}}})
	parent := mint("example", "definitions", "configure")
	body := &gen.ModuleExecutableArtifactRequest{ParentWorkContextToken: parent, InstallationId: uuid.NewString(), PolicyId: "content", Identity: &gen.ExecutableArtifactIdentity{Schema: policy.Schema, Source: "acme.example", Subject: []byte(`{"configuration":"pinned","scripts":[]}`), ExpectedRevision: 9007199254740993, Contracts: []*gen.ExecutableArtifactContract{{Kind: "model", Name: "example/chat", Digest: digest}}}}
	request := func(parent string) *connect.Request[gen.ModuleExecutableArtifactRequest] {
		r := connect.NewRequest(proto.Clone(body).(*gen.ModuleExecutableArtifactRequest))
		r.Msg.ParentWorkContextToken = parent
		headers := readExchangeRequest(t, parent).Header()
		for k, v := range headers {
			r.Header()[k] = append([]string{}, v...)
		}
		return r
	}
	runParent := mint("example", "definitions", "run")
	_, err = client.AuthorizeExecutableArtifact(context.Background(), request(runParent))
	require.ErrorContains(t, err, "exact artifact approval")
	for _, tc := range []struct {
		name   string
		change func(*connect.Request[gen.ModuleExecutableArtifactRequest])
	}{
		{"no perimeter", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) {
			r.Header().Del("x-codefly-internal-token")
		}},
		{"forged parent", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) { r.Msg.ParentWorkContextToken = "forged" }},
		{"run cannot consent", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) {
			r.Msg.ParentWorkContextToken = runParent
		}},
		{"foreign audience", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) {
			r.Msg.ParentWorkContextToken = mint("foreign", "definitions", "configure")
		}},
		{"unknown policy", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) { r.Msg.PolicyId = "foreign" }},
		{"foreign source", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) { r.Msg.Identity.Source = "foreign" }},
		{"contract changed", func(r *connect.Request[gen.ModuleExecutableArtifactRequest]) {
			r.Msg.Identity.Contracts[0].Digest = "sha256:" + strings.Repeat("b", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(parent)
			tc.change(r)
			_, e := client.ApproveExecutableArtifact(context.Background(), r)
			require.Error(t, e)
			require.Empty(t, store.rows)
		})
	}
	store.admin = false
	_, err = client.ApproveExecutableArtifact(context.Background(), request(parent))
	require.ErrorContains(t, err, "artifact authority")
	store.admin = true
	approved, err := client.ApproveExecutableArtifact(context.Background(), request(parent))
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), approved.Msg.ExpectedRevision)
	authorized, err := client.AuthorizeExecutableArtifact(context.Background(), request(runParent))
	require.NoError(t, err)
	require.Equal(t, approved.Msg.QualifiedName, authorized.Msg.QualifiedName)
	require.Equal(t, approved.Msg.Digest, authorized.Msg.Digest)
	facts.facts.OrganizationRevision++
	_, err = client.AuthorizeExecutableArtifact(context.Background(), request(runParent))
	require.ErrorContains(t, err, "stale")
	facts.facts.OrganizationRevision--
	revoke := connect.NewRequest(&gen.ModuleRevokeExecutableArtifactRequest{ParentWorkContextToken: parent, InstallationId: body.InstallationId, ApprovalId: strings.TrimPrefix(approved.Msg.QualifiedName, "host/approved-artifacts/")})
	for k, v := range request(parent).Header() {
		revoke.Header()[k] = v
	}
	_, err = client.RevokeExecutableArtifact(context.Background(), revoke)
	require.NoError(t, err)
	_, err = client.AuthorizeExecutableArtifact(context.Background(), request(runParent))
	require.ErrorContains(t, err, "revoked")
	_, err = client.ApproveExecutableArtifact(context.Background(), request(parent))
	require.ErrorContains(t, err, "revoked")
}

func (s *artifactTransportStore) GetExecutableArtifactApprovalByID(_ context.Context, _, _, _, id string) (*business.ExecutableArtifactApproval, error) {
	for _, row := range s.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return nil, nil
}

func (s *artifactTransportStore) CheckExecutableArtifactAuthority(_ context.Context, _, _, _ string, _ business.ArtifactPermission, admin bool) (bool, error) {
	return s.member && (!admin || s.admin), nil
}

type artifactTransportStoreScope struct {
	business.Scoped
	store *artifactTransportStore
	id    business.Identity
}

func (s *artifactTransportStore) As(id business.Identity) business.Scoped {
	return &artifactTransportStoreScope{store: s, id: id}
}
func (s *artifactTransportStoreScope) Within(ctx context.Context, fn func(context.Context) error) error {
	return s.store.WithOrgTx(ctx, s.id.OrgID, fn)
}
