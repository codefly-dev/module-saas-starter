package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
)

type stepServiceFake struct {
	claimed, finished int
	leaseValid        bool
	mintErr           error
	status            int
}

func (f *stepServiceFake) ClaimInstallationStep(context.Context, string) (*jobsv1.JobEnvelope, error) {
	f.claimed++
	return &jobsv1.JobEnvelope{Id: installTaskID, AttemptCount: 1, Lease: &jobsv1.JobLease{Token: installSession}}, nil
}
func (f *stepServiceFake) WithInstallationStep(ctx context.Context, lease *jobsv1.JobLeaseReference, mint func(context.Context, *business.InstallationStepAuthority, []*gen.WorkContextScope) error) error {
	if f.mintErr != nil {
		return f.mintErr
	}
	return mint(ctx, &business.InstallationStepAuthority{InstallationStep: business.InstallationStep{InstallationID: installID, OrganizationID: installOrgID, Revision: 7}, TargetID: "example-target", RouteAlias: "example", BindingID: "example-binding"}, nil)
}
func (f *stepServiceFake) ValidateInstallationStepLease(context.Context, *jobsv1.JobLeaseReference) error {
	if !f.leaseValid {
		return errors.New("wrong queue or expired lease")
	}
	return nil
}
func (f *stepServiceFake) FinishInstallationStep(_ context.Context, _ *jobsv1.JobLeaseReference, _ uint32, status int, _ string) error {
	f.finished++
	f.status = status
	return nil
}

// HOST-INST-003: only the private consumer can claim. Wire input cannot choose
// an installation, scope or audience, and an invalid lease finalizes nothing.
func TestInstallationStepHTTPIsPrivateAndLeaseBound(t *testing.T) {
	old := internalToken
	SetInternalToken("example-internal")
	t.Cleanup(func() { SetInternalToken(old) })
	f := &stepServiceFake{}
	h := installationStepHandler(f, func(context.Context, *business.InstallationStepAuthority, []*gen.WorkContextScope, *jobsv1.JobLeaseReference) (string, error) {
		return "scoped-token", nil
	})
	call := func(path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, InstallationStepPath+path, strings.NewReader(body))
		r.Header.Set("X-Codefly-Internal-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, 401, call("claim", `{"worker":"example"}`, "forged").Code)
	require.Zero(t, f.claimed)
	require.Equal(t, 400, call("claim", `{"worker":"example","installation_id":"foreign"}`, "example-internal").Code)
	require.Zero(t, f.claimed)
	w := call("claim", `{"worker":"example"}`, "example-internal")
	require.Equal(t, 200, w.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, "scoped-token", out["work_context"])
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	body := `{"job_id":"` + installTaskID + `","worker":"example","lease_token":"` + installSession + `","status":204}`
	require.Equal(t, 409, call("finish", body, "example-internal").Code)
	require.Zero(t, f.finished)
	f.leaseValid = true
	require.Equal(t, 204, call("finish", body, "example-internal").Code)
	require.Equal(t, 204, f.status)
	f.mintErr = errors.New("authority unavailable")
	require.Equal(t, 503, call("claim", `{"worker":"example"}`, "example-internal").Code)
	require.Equal(t, 503, f.status)
}

// HOST-INST-003: the install step reuses the headless issuer. Its task names
// exactly one installation, its sole actor is that installation's agent, and
// its scope names only its own root. No-scope setup is valid and grants nothing.
func TestInstallationStepSignedAuthority(t *testing.T) {
	s := newInstallationMintServer(t, healthyInstallationFacts(), nil)
	for _, scopes := range [][]*gen.WorkContextScope{nil, {{ResourceKind: "doc", Actions: []string{"write"}, ResourceIds: []string{"own-root"}}}} {
		_, core, err := workContextScopes(scopes, noContentReads)
		require.NoError(t, err)
		req := installTaskRequest()
		req.TaskId = installID
		req.TtlSeconds = 60
		issued, err := s.issueInstallationTask(context.Background(), req, healthyInstallationFacts(), core)
		require.NoError(t, err)
		token, err := workcontext.ParseWorkContextToken(issued.Token)
		require.NoError(t, err)
		claims, err := s.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: "accounts.test", Audience: req.Audience, TenantID: installOrgID})
		require.NoError(t, err)
		require.Equal(t, installID, claims.GetTaskId())
		require.Equal(t, installAgentID, claims.GetActorChain()[0].GetPrincipalId())
		require.Len(t, claims.GetAuthorityScopes(), len(scopes))
		if len(scopes) > 0 {
			require.Equal(t, []string{"own-root"}, claims.GetAuthorityScopes()[0].GetResourceIds())
		}
	}
}
