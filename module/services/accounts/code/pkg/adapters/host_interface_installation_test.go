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
// interface, the solution's workload credential and the module's.
func installationIssueFixture(t *testing.T) (*HostAuthorityServer, *sealAnswersFake, string, string) {
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
	issuer := newCoreTestServer(t, &workContextAuthorityFake{installationFcts: facts}, seals)
	return NewHostAuthorityServer(issuer, nil), seals,
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
	server, seals, solution, module := installationIssueFixture(t)
	seals.uninstalled["uninstalled.test"] = true
	for name, tc := range map[string]struct {
		req  *authorityv1.IssueRequest
		code codes.Code
		says string
	}{
		"no actor":                   {installationIssue(notesInstallation, "", ""), codes.Unauthenticated, "carries no actor credential"},
		"a forged actor":             {installationIssue(notesInstallation, "forged", ""), codes.PermissionDenied, "credential refused"},
		"another module's workload":  {installationIssue(notesInstallation, module, ""), codes.PermissionDenied, "is not tool.test's active installation"},
		"another installation":       {installationIssue(toolInstallation, solution, ""), codes.PermissionDenied, "is not notes.test's active installation"},
		"an unknown installation":    {installationIssue("0199a000-0000-7000-8000-000000000000", solution, ""), codes.PermissionDenied, "acts only for itself"},
		"another organisation":       {installationIssue(notesInstallation, solution, "0199a000-0000-7000-8000-0000000000ff"), codes.PermissionDenied, "acts in organization " + renewOrgID},
		"no installation of its own": {installationIssue(notesInstallation, solution, ""), codes.PermissionDenied, "has no active installation"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "no installation of its own" {
				seals.uninstalled["notes.test"] = true
				t.Cleanup(func() { delete(seals.uninstalled, "notes.test") })
			}
			out, err := server.Issue(context.Background(), tc.req)
			require.Nil(t, out)
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), tc.says)
		})
	}
}
