package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
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
	}, agents: map[string]string{installAgentID: notesInstallation}}
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
// capability, sealed to that installation and owned by its agent: no person is
// acting, so neither the owner of record nor anyone else is named, and nothing
// acts below the owner. It verifies where it is addressed.
func TestAnInstallationActsForItselfThroughItsSolutionsWorkload(t *testing.T) {
	server, _, solution, _ := installationIssueFixture(t)
	out, err := server.Issue(context.Background(), installationIssue(notesInstallation, solution, ""))
	require.NoError(t, err)
	decoded, err := workcontext.Decode(out.GetToken())
	require.NoError(t, err)
	wc := decoded.Context()
	require.Equal(t, notesInstallation, wc.GetSeal().GetInstallationId())
	require.Equal(t, renewOrgID, wc.GetTenantId())
	require.Equal(t, installAgentID, wc.GetOwnerPrincipalId())
	require.Equal(t, business.PrincipalKindAgent, wc.GetOwnerPrincipalKind())
	require.Equal(t, "acme.example/solution:1.0.0", wc.GetOwnerAgentId())
	require.Empty(t, wc.GetActorChain())
	require.Empty(t, wc.GetAttributionTeamIds(), "no person's teams are attributed")
	require.Equal(t, installAgentID, out.GetContext().GetOwnerPrincipalId())
	require.Equal(t, "agent", out.GetContext().GetOwnerPrincipalKind())
	_, err = server.Check(context.Background(), &authorityv1.CheckRequest{Token: out.GetToken(), Audience: "notes.test"})
	require.NoError(t, err)
}

// A Check re-reads the installation an agent-owned capability stands on:
// revoking the installation, withdrawing what its standing grant admits, or
// moving the installation to another agent refuses a capability whose seal and
// revision have not moved.
func TestACheckReverifiesAnInstallationOwnedCapability(t *testing.T) {
	for name, move := range map[string]func(*workContextAuthorityFake){
		"the installation is revoked": func(f *workContextAuthorityFake) {
			revoked := f.installations[notesInstallation]
			revoked.Status = "revoked"
			f.installations[notesInstallation] = revoked
		},
		"the standing grant no longer admits it": func(f *workContextAuthorityFake) {
			f.installationErr = business.NewStoreError(errors.New("agent standing grant does not authorize doc:read at requested scope"), business.ErrTypePermission)
		},
		"the installation acts through another agent": func(f *workContextAuthorityFake) {
			moved := *f.installationFcts
			actor := *moved.Actor
			actor.ID = "019fec91-2000-7000-8000-0000000000ff"
			moved.Actor = &actor
			f.installationFcts = &moved
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, store, solution, _ := installationIssueFixture(t)
			out, err := server.Issue(context.Background(), installationIssue(notesInstallation, solution, ""))
			require.NoError(t, err)
			move(store)
			_, err = server.Check(context.Background(), &authorityv1.CheckRequest{Token: out.GetToken(), Audience: "notes.test"})
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			_, err = server.ListResources(context.Background(), &authorityv1.ListResourcesRequest{Token: out.GetToken(), Audience: "notes.test", ResourceKind: "doc", Action: "read"})
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		})
	}
}

// An installation's capability cannot be used to act as a person: a member
// signed in cannot renew it or exchange it as their own, because it is not
// theirs, and a hop below it stays owned by the agent
// (TestAHopFromAnInstallationOwnedParentInheritsTheInstallationsStandingGrants).
func TestAnInstallationOwnedCapabilityCannotActAsAPerson(t *testing.T) {
	server, _, solution, _ := installationIssueFixture(t)
	out, err := server.Issue(context.Background(), installationIssue(notesInstallation, solution, ""))
	require.NoError(t, err)
	member := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, auth.Assurance{})
	for name, audience := range map[string]string{"a renewal": "notes.test", "an exchange": "tool.test"} {
		t.Run(name, func(t *testing.T) {
			_, err := server.Issue(member, &authorityv1.IssueRequest{
				As: &authorityv1.IssueRequest_ParentToken{ParentToken: out.GetToken()}, Audience: audience,
			})
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), "not valid for caller")
		})
	}
}
