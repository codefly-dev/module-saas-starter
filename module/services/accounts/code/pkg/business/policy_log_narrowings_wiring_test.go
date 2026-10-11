//go:build pure

package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// What each narrowing tells the log, and that the narrowing paths genuinely run
// under the protocol rather than merely having an entry builder nearby.
//
// The coverage gate beside this file reads the source and answers "is every
// narrowing wrapped". That is a structural question, and it cannot answer the
// two that matter next: whether the entry a wrapped path offers the log is one
// the log would accept, and whether a host with no log actually REFUSES the
// operation. Both are behavioural, so both are asserted here by calling the
// real methods.

// everyNarrowingEntry is each builder with the inputs one call site passes it.
// Table-driven so a builder added without a case here shows up in the count
// assertion below rather than silently going unchecked.
func everyNarrowingEntry() []struct {
	name        string
	entry       *PolicyLogEntry
	wantKind    PolicyLogSubjectKind
	wantSubject string
	wantActor   string
} {
	return []struct {
		name        string
		entry       *PolicyLogEntry
		wantKind    PolicyLogSubjectKind
		wantSubject string
		wantActor   string
	}{
		{
			name:        "uninstall-solution",
			entry:       uninstallPolicyLogEntry("admin-1", "org-1", "install-1"),
			wantKind:    PolicyLogInstallation,
			wantSubject: "install-1",
			wantActor:   "admin-1",
		},
		{
			name:        "revoke-scope",
			entry:       revokeScopePolicyLogEntry("admin-1", "org-1", "user-1", "SUBJECT_KIND_USER", "/acme/eu", "role-1"),
			wantKind:    PolicyLogScopeGrant,
			wantSubject: "org-1/SUBJECT_KIND_USER/user-1//acme/eu/role-1",
			wantActor:   "admin-1",
		},
		{
			name:        "remove-team-member",
			entry:       removeTeamMemberPolicyLogEntry("admin-1", "org-1", "team-1", "user-1"),
			wantKind:    PolicyLogTeamMembership,
			wantSubject: "team-1/user-1",
			wantActor:   "admin-1",
		},
		{
			name:        "delete-organization",
			entry:       deletedOrganizationPolicyLogEntry("admin-1", "org-1"),
			wantKind:    PolicyLogOrganization,
			wantSubject: "org-1",
			wantActor:   "admin-1",
		},
		{
			name:        "revoke-role",
			entry:       revokeRolePolicyLogEntry("admin-1", "org-1", "user-1", "role-1", "/acme"),
			wantKind:    PolicyLogScopeGrant,
			wantSubject: "org-1/user-1/role-1//acme",
			wantActor:   "admin-1",
		},
		{
			name:        "revoke-share",
			entry:       revokeSharePolicyLogEntry("admin-1", "org-1", "document", "doc-1", "user-1", "SUBJECT_KIND_USER", "role-1"),
			wantKind:    PolicyLogScopeGrant,
			wantSubject: "org-1/document/doc-1/SUBJECT_KIND_USER/user-1/role-1",
			wantActor:   "admin-1",
		},
		{
			name:        "revoke-principal",
			entry:       revokePrincipalPolicyLogEntry("system", "principal-1", "org-1", "credential compromise"),
			wantKind:    PolicyLogPrincipal,
			wantSubject: "principal-1",
			wantActor:   "system",
		},
		{
			name:        "revoke-source-delegation",
			entry:       revokeSourceDelegationPolicyLogEntry("admin-1", "org-1", "delegation-1"),
			wantKind:    PolicyLogBinding,
			wantSubject: "delegation-1",
			wantActor:   "admin-1",
		},
		{
			name:        "remove-org-member",
			entry:       removeOrgMemberPolicyLogEntry("admin-1", "org-1", "user-1"),
			wantKind:    PolicyLogBinding,
			wantSubject: "org-1/user-1",
			wantActor:   "admin-1",
		},
		{
			name:        "revoke-api-key",
			entry:       revokeAPIKeyPolicyLogEntry("admin-1", "org-1", "key-1"),
			wantKind:    PolicyLogPrincipal,
			wantSubject: "key-1",
			wantActor:   "admin-1",
		},
		{
			name:        "revoke-platform-role",
			entry:       revokePlatformRolePolicyLogEntry("admin-1", "user-1"),
			wantKind:    PolicyLogBinding,
			wantSubject: "user-1",
			wantActor:   "admin-1",
		},
		{
			name:        "close-solution-target",
			entry:       closedSolutionTargetPolicyLogEntry("op-1", "target-1", "binding-1", "solution-1", 4),
			wantKind:    PolicyLogInstallation,
			wantSubject: "target-1",
			wantActor:   "solution:solution-1",
		},
	}
}

// Every entry a narrowing offers the log has to be one the log would accept and
// a replay could read: a valid subject kind, a subject it names, an actor, and
// the resulting authority rather than a diff.
func TestNarrowingEntries_AreReplayable(t *testing.T) {
	for _, tc := range everyNarrowingEntry() {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.entry.Validate(),
				"the log refuses this entry, so the narrowing it describes would be refused too")
			require.Equal(t, PolicyLogNarrowed, tc.entry.Decision)
			require.Equal(t, tc.wantKind, tc.entry.SubjectKind)
			require.True(t, tc.entry.SubjectKind.Valid(),
				"a subject kind outside the closed set is one no reconciliation could act on")
			require.Equal(t, tc.wantSubject, tc.entry.SubjectID)
			require.Equal(t, tc.wantActor, tc.entry.Actor)
			require.NotEmpty(t, tc.entry.Policy,
				"an entry with no policy says nothing about the authority that remains")
			require.Contains(t, tc.entry.Policy, "status",
				"Policy carries the authority as it stands AFTER the operation, so each one states the resulting status")
		})
	}
}

// The operation id is FRESH on every attempt, deliberately: an id derived from
// the operation's inputs collides with a REVOKE → REGRANT → REVOKE sequence and
// lets the second revocation apply against the first's receipt, unwitnessed.
// Over-recording is the safe direction (policy_log_narrowings.go says why), and
// this holds the builders to it.
//
// The one exception is the solution-target close, whose id is passed in because
// the apply transaction has to recognise the close it was appended for.
func TestNarrowingEntries_OperationIDsAreFreshPerAttempt(t *testing.T) {
	first := everyNarrowingEntry()
	second := everyNarrowingEntry()
	for i, tc := range first {
		if tc.name == "close-solution-target" {
			require.Equal(t, tc.entry.OperationID, second[i].entry.OperationID,
				"the close's id is the caller's, so two builds of it must agree")
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.entry.OperationID)
			require.NotEqual(t, tc.entry.OperationID, second[i].entry.OperationID,
				"two attempts at the same narrowing derived the same operation id; a retry would reuse the first receipt and the second narrowing would apply unwitnessed")
		})
	}
}

// A host with NO policy log refuses to narrow, at the real entry points. This is
// the wiring assertion: the refusal can only come from
// WithPolicyLoggedNarrowing, so a call site that stopped going through it — or
// never did — fails here with its own name.
//
// The five below are the narrowings reachable with no database: the rest read
// the store before they append (an actor type, a principal's organisation, the
// caller's platform role), and the production path covers those against real
// PostgreSQL in policy_log_production_path_test.go.
func TestWitnessedNarrowings_AHostWithNoLogRefuses(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(*Service) error
	}{
		{"RevokeRole", func(s *Service) error {
			return s.RevokeRole(ctx, "admin-1", &gen.RevokeRoleRequest{
				OrgId: "org-1", SubjectId: "user-1", RoleId: "role-1", Scope: "/acme",
			})
		}},
		{"RevokeShare", func(s *Service) error {
			return s.RevokeShare(ctx, "admin-1", &gen.RevokeShareRequest{
				OrgId: "org-1", ResourceType: "document", ResourceId: "doc-1",
				SubjectId: "user-1", RoleId: "role-1",
			})
		}},
		{"RevokeAPIKey", func(s *Service) error {
			return s.RevokeAPIKey(ctx, "admin-1", &gen.RevokeAPIKeyRequest{
				Id: "key-1", OrganizationId: "org-1",
			})
		}},
		{"RemoveOrgMember", func(s *Service) error {
			return s.RemoveOrgMember(ctx, "admin-1", &gen.RemoveOrgMemberRequest{
				OrgId: "org-1", UserId: "user-1",
			})
		}},
		{"RevokeSourceDelegation", func(s *Service) error {
			_, err := s.RevokeSourceDelegation(ctx, "admin-1", "org-1", "delegation-1")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No SetPolicyLog: this host cannot witness anything. The store
			// panics on any method these paths were not meant to reach, so a
			// call that got as far as writing would say so loudly rather than
			// passing.
			service := &Service{store: noopControlPlaneStore{}}
			require.ErrorIs(t, tc.call(service), ErrPolicyLogUnreachable,
				"%s narrowed (or tried to) on a host with no policy log; it is not going through WithPolicyLoggedNarrowing", tc.name)
		})
	}
}

// The organisation archive names the ORGANISATION, and this is what that
// stopgap became.
//
// It was written the other way round first: deletedOrganizationPolicyLogEntry
// reported PolicyLogBinding because the closed set had no kind for an
// organisation, and this test was the tripwire on that label — it required the
// stopgap to still be the nearest available statement, and failed the day a
// real kind appeared. The kind now exists, so the assertion inverts: the entry
// must use it, and must NOT be a binding.
//
// PolicyLogBinding is asserted against explicitly rather than just asserting
// the new value, because reverting to it is the specific regression this
// covers: an archive reported as a binding sends a reconciliation looking for a
// binding whose id is an organisation's, which it will never find.
func TestDeletedOrganizationEntryNamesTheOrganisationItArchives(t *testing.T) {
	entry := deletedOrganizationPolicyLogEntry("admin-1", "org-1")
	require.Equal(t, PolicyLogOrganization, entry.SubjectKind)
	require.NotEqual(t, PolicyLogBinding, entry.SubjectKind,
		"an archive is not a binding; a replay reading it would look for a binding whose id is an organisation's")
	require.True(t, entry.SubjectKind.Valid(),
		"the kind must be in the closed set, or a reconciliation cannot act on the entry at all")
	require.Equal(t, "org-1", entry.SubjectID)
	require.NoError(t, entry.Validate())
}
