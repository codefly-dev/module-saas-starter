package adapters

import (
	"context"
	"testing"

	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// installationIssueFixture is a host composing one solution, notes.test, pinned
// to the test organisation and installed there (the seal fake answers
// notesInstallation), beside the module tool.test. It returns the
// interface, the store answering whose each installation is, the solution's
// workload credential and the module's.
func installationIssueFixture(t *testing.T) (*HostAuthorityServer, *workContextAuthorityFake, string, string) {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster)
	solution := business.ModuleWorkload{Namespace: "notes", ServiceAccount: "notes", Container: "app"}
	module := business.ModuleWorkload{Namespace: "example", ServiceAccount: "tool", Container: "app"}
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{
		business.ModulePrincipalID("notes.test"): {Prefix: "notes.test", Tenant: renewOrgID, Resources: []string{"doc"}, Workloads: []business.ModuleWorkload{solution}},
		business.ModulePrincipalID("tool.test"):  {Prefix: "tool.test", Tenant: renewOrgID, Workloads: []business.ModuleWorkload{module}},
	})
	declareTestAudiences(svc)
	seals := newSealAnswersFake(7)
	seals.installations["notes.test"] = notesInstallation
	seals.installations["tool.test"] = toolInstallation
	facts := healthyInstallationFacts()
	facts.Actor.AllowedAudiences, facts.Actor.AllowedScopes = nil, nil
	authority := &workContextAuthorityFake{installationFcts: facts, installations: map[string]business.InstallationIdentity{
		notesInstallation: {OrgID: renewOrgID, PrincipalID: business.ModulePrincipalID("notes.test"), Status: "active"},
		toolInstallation:  {OrgID: renewOrgID, PrincipalID: business.ModulePrincipalID("tool.test"), Status: "active"},
	}}
	issuer := newCoreTestServer(t, authority, seals)
	return NewHostAuthorityServer(issuer, nil), authority,
		cluster.Run(executiontest.Workload{Namespace: solution.Namespace, ServiceAccount: solution.ServiceAccount, Container: solution.Container}),
		cluster.Run(executiontest.Workload{Namespace: module.Namespace, ServiceAccount: module.ServiceAccount, Container: module.Container})
}

const (
	notesInstallation = "0199a000-0000-7000-8000-0000000000a1"
	toolInstallation  = "0199a000-0000-7000-8000-0000000000a2"
)

func installationIssue(installation, credential, org string) *authorityv1.IssueRequest {
	return &authorityv1.IssueRequest{
		As:       &authorityv1.IssueRequest_Installation{Installation: &authorityv1.InstallationRef{InstallationId: installation}},
		Audience: "notes.test", OrganizationId: org, Actor: &authorityv1.WorkloadCredential{Credential: credential},
		Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "doc", Actions: []string{"read"}}},
	}
}

// The installed solution's own workload is issued its own installation's
// capability, sealed to that installation and actored by its agent.
func TestAnInstallationActsForItselfThroughItsSolutionsWorkload(t *testing.T) {
	server, _, solution, _ := installationIssueFixture(t)
	out, err := server.Issue(context.Background(), installationIssue(notesInstallation, solution, ""))
	require.NoError(t, err)
	decoded, err := workcontext.Decode(out.GetToken())
	require.NoError(t, err)
	require.Equal(t, notesInstallation, decoded.Context().GetSeal().GetInstallationId())
	require.Equal(t, renewOrgID, decoded.Context().GetTenantId())
	require.Equal(t, installAgentID, decoded.Context().GetActorChain()[0].GetPrincipalId())
}

// An installation id is not a credential: an issue naming one is refused by
// name, before any mint, unless its actor is that installation's own solution
// and the installation is that solution's active one in the organisation.
func TestAnInstallationIssueIsRefusedUnlessTheCallerIsItsOwnSolution(t *testing.T) {
	server, store, solution, module := installationIssueFixture(t)
	for name, tc := range map[string]struct {
		req    *authorityv1.IssueRequest
		status string
		code   codes.Code
		says   string
	}{
		"no actor":                  {req: installationIssue(notesInstallation, "", ""), code: codes.Unauthenticated, says: "carries no actor credential"},
		"a forged actor":            {req: installationIssue(notesInstallation, "forged", ""), code: codes.PermissionDenied, says: "credential refused"},
		"another module's workload": {req: installationIssue(notesInstallation, module, ""), code: codes.PermissionDenied, says: "tool.test is not the installed solution"},
		"another installation":      {req: installationIssue(toolInstallation, solution, ""), code: codes.PermissionDenied, says: "notes.test is not the installed solution"},
		"an unknown installation":   {req: installationIssue("conformance-missing-installation", solution, ""), code: codes.NotFound, says: `installation "conformance-missing-installation" does not exist`},
		"another organisation":      {req: installationIssue(notesInstallation, solution, "0199a000-0000-7000-8000-0000000000ff"), code: codes.PermissionDenied, says: "belongs to organization " + renewOrgID},
		"a pending installation":    {req: installationIssue(notesInstallation, solution, ""), status: "pending", code: codes.PermissionDenied, says: "is pending"},
		"a revoked installation":    {req: installationIssue(notesInstallation, solution, ""), status: "revoked", code: codes.PermissionDenied, says: "is revoked"},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.status != "" {
				active := store.installations[notesInstallation]
				moved := active
				moved.Status = tc.status
				store.installations[notesInstallation] = moved
				t.Cleanup(func() { store.installations[notesInstallation] = active })
			}
			out, err := server.Issue(context.Background(), tc.req)
			require.Nil(t, out)
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), tc.says)
		})
	}
}

// An installation's caller is proven before anything it asks is read: one
// proving nothing is refused for that, even when it names no installation and
// no audience, so it learns nothing of how its ask would have been judged. A
// proven caller is then told what its ask lacks, by name.
func TestAnInstallationIssueProvesItsCallerBeforeReadingTheAsk(t *testing.T) {
	server, _, solution, _ := installationIssueFixture(t)
	bare := func(installation, credential, audience string) *authorityv1.IssueRequest {
		req := installationIssue(installation, credential, "")
		req.Audience = audience
		return req
	}
	for name, tc := range map[string]struct {
		req  *authorityv1.IssueRequest
		code codes.Code
		says string
	}{
		"no actor, nothing named":        {bare("", "", ""), codes.Unauthenticated, "carries no actor credential"},
		"a forged actor, nothing named":  {bare("", "forged", ""), codes.PermissionDenied, "credential refused"},
		"proven, no installation":        {bare("", solution, "notes.test"), codes.InvalidArgument, "names the installation acting"},
		"proven, no audience":            {bare(notesInstallation, solution, ""), codes.InvalidArgument, "names its audience"},
		"proven, an uncomposed audience": {bare(notesInstallation, solution, "nowhere.test"), codes.NotFound, "is not composed"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := server.Issue(context.Background(), tc.req)
			require.Nil(t, out)
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), tc.says)
		})
	}
}
