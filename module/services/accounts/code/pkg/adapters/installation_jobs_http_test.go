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

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// installStepLease is the job lease the step's consumer holds. It is the
// job's, never a session: the capability's session is the issuer's own.
const installStepLease = "019fec91-2000-7000-8000-0000000000a6"

type stepServiceFake struct {
	claimed, finished int
	leaseValid        bool
	mintErr           error
	status            int
}

func (f *stepServiceFake) ClaimInstallationStep(context.Context, string) (*jobsv1.JobEnvelope, error) {
	f.claimed++
	return &jobsv1.JobEnvelope{Id: installTaskID, AttemptCount: 1, Lease: &jobsv1.JobLease{Token: installStepLease}}, nil
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
	body := `{"job_id":"` + installTaskID + `","worker":"example","lease_token":"` + installStepLease + `","status":204}`
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
// its scope names only its own root and the step itself. No-scope setup is
// valid: it grants the step and nothing else.
func TestInstallationStepSignedAuthority(t *testing.T) {
	s := newInstallationMintServer(t, healthyInstallationFacts(), nil)
	for _, scopes := range [][]*gen.WorkContextScope{nil, {{ResourceKind: "doc", Actions: []string{"write"}, ResourceIds: []string{"own-root"}}}} {
		_, core, err := workContextScopes(scopes, noContentReads)
		require.NoError(t, err)
		req := installTaskRequest()
		req.TaskId = installID
		req.TtlSeconds = 60
		issued, err := s.signInstallationStep(context.Background(), req, healthyInstallationFacts(), core)
		require.NoError(t, err)
		verified, err := s.verifyIssued(context.Background(), issued.GetToken())
		require.NoError(t, err)
		claims := verified.Context()
		require.Equal(t, installOrgID, claims.GetTenantId())
		require.Equal(t, installID, claims.GetTaskId())
		require.Len(t, claims.GetActorChain(), 1)
		require.Equal(t, installAgentID, claims.GetActorChain()[0].GetPrincipalId())
		// Sealed to the one installation the step is for: the carrier the
		// gateway sends beside it names this id and revision, and the
		// solution's runtime refuses a step whose carrier and seal disagree.
		require.Equal(t, req.GetInstallationId(), claims.GetSeal().GetInstallationId())
		// The agent is delegated the installation's scopes and the step's own,
		// and nothing else — so a scope-less installation still has a step.
		granted := claims.GetActorChain()[0].GetGrantedScopes()
		require.Len(t, granted, len(scopes)+1)
		var own, step int
		for _, scope := range granted {
			switch scope.GetResourceKind() {
			case InstallationStepResourceKind:
				step++
				require.Equal(t, []string{InstallationStepAction}, scope.GetActions())
				require.Equal(t, []string{req.GetInstallationId()}, scope.GetResourceIds())
			default:
				own++
				require.Equal(t, []string{"own-root"}, scope.GetResourceIds())
			}
		}
		require.Equal(t, 1, step)
		require.Equal(t, len(scopes), own)
	}
}

// An installation's own scope may not spell the step's kind: the step's scope is
// the host's to add, for this installation only, so a scope arriving with it
// would be a second, caller-shaped claim on the same word.
func TestInstallationStepRefusesAScopeNamingTheStepItself(t *testing.T) {
	s := newInstallationMintServer(t, healthyInstallationFacts(), nil)
	req := installTaskRequest()
	req.TaskId = installID
	_, err := s.signInstallationStep(context.Background(), req, healthyInstallationFacts(), []*basev0.WorkScopeV1{{
		ResourceKind: InstallationStepResourceKind, Actions: []string{InstallationStepAction}, ResourceIds: []string{"another-installation"},
	}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// The step's kind is the issuer's to stamp on every path. An ordinary
// installation task that could name it would carry a second spelling of "run
// this installation's install step" — on another installation's id.
func TestNoInstallationMintAcceptsAScopeNamingTheInstallStep(t *testing.T) {
	for _, kind := range []string{"installations", "Installations", "INSTALLATIONS"} {
		for name, ceiling := range map[string][]string{"a ceiling naming the kind": {"doc", "installations"}, "no ceiling": nil} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				facts := healthyInstallationFacts()
				facts.Actor.AllowedScopes = ceiling
				server := newInstallationMintServer(t, facts, nil)
				t.Cleanup(func() { SetInternalToken("") })
				req := installTaskRequest()
				req.AuthorityScopes = []*gen.WorkContextScope{{
					ResourceKind: kind, Actions: []string{InstallationStepAction}, ResourceIds: []string{"another-installation"},
				}}
				_, err := server.StartInstallationTask(installInternalContext(), req)
				require.Equal(t, codes.InvalidArgument, status.Code(err), "an ordinary installation task minted the install step's scope: %v", err)

				step := installTaskRequest()
				step.TaskId = installID
				_, err = server.signInstallationStep(context.Background(), step, facts, []*basev0.WorkScopeV1{{
					ResourceKind: kind, Actions: []string{InstallationStepAction}, ResourceIds: []string{"another-installation"},
				}})
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			})
		}
	}
}
