//go:build !pure

package business_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	authcore "accounts/pkg/auth"

	"github.com/google/uuid"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// tenancyWorld is organization A with an administrator, a member and a role it
// can see; organization B with a team; and an identity that is not a member of
// A, holding a session bound to no organization — the kind of session a write
// naming them in A would reach.
type tenancyWorld struct {
	admin, member, org, role string
	otherTeam                string
	outsider                 string
	outsiderSession          []byte
}

func newTenancyWorld(t *testing.T) *tenancyWorld {
	t.Helper()
	clearData(t)
	adapters.WithService(testService)
	w := &tenancyWorld{}
	w.admin, w.org = mustUserAndOrg(t, testCtx, "admin@subject-a.test", "subject-a-admin", "Subject A")
	w.member = mustMember(t, w.admin, w.org, "member@subject-a.test", "subject-a-member")
	otherAdmin, otherOrg := mustUserAndOrg(t, testCtx, "admin@subject-b.test", "subject-b-admin", "Subject B")
	team, err := testService.CreateTeam(testCtx, otherAdmin, &gen.CreateTeamRequest{OrgId: otherOrg, Name: "elsewhere"})
	require.NoError(t, err)
	w.otherTeam = team.Team.Id

	w.role = business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, w.org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: w.role, Name: "subject reader " + w.role, OrgId: w.org,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
		})
	}))

	w.outsider = mustUser(t, "outsider@subject.test", "subject-outsider")
	hash := sha256.Sum256([]byte("outsider-refresh-" + w.outsider))
	w.outsiderSession = hash[:]
	now := time.Now()
	require.NoError(t, testSessionStore.Insert(testCtx, &authcore.SessionRecord{
		UserID:          uuid.MustParse(w.outsider),
		FamilyID:        uuid.New(),
		RefreshHash:     w.outsiderSession,
		AuthenticatedAt: now,
		IdleExpiresAt:   now.Add(time.Hour),
		ExpiresAt:       now.Add(2 * time.Hour),
	}))
	w.requireOutsiderSessionIntact(t)
	return w
}

// requireOutsiderSessionIntact proves the outsider's unbound session was not
// revoked.
func (w *tenancyWorld) requireOutsiderSessionIntact(t *testing.T) {
	t.Helper()
	session, err := testSessionStore.FindByRefreshHash(testCtx, w.outsiderSession)
	require.NoError(t, err)
	require.Nil(t, session.RevokedAt, "a refused write must leave the subject's sessions untouched (revoked: %q)", session.RevokedReason)
}

// awaitBlockedTransaction proves two transactions genuinely contend.
func awaitBlockedTransaction(t *testing.T, msg string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		if err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.Pool().QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted)`).Scan(&waiting)
		}); err != nil {
			return false
		}
		return waiting
	}, 10*time.Second, 20*time.Millisecond, msg)
}

func TestAssignRoleRequiresTheSubjectToBelongToTheOrganization(t *testing.T) {
	w := newTenancyWorld(t)
	server := &adapters.PermServer{}
	caller := storyCallerContext(testCtx, w.admin, w.org)

	// A scoped assignment is the one whose write also touches the subject's
	// sessions; it is refused before it is written.
	_, err := server.AssignRole(caller, &gen.AssignRoleRequest{
		SubjectId: w.outsider, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		RoleId: w.role, OrgId: w.org, Scope: "docs",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "got %v", err)
	w.requireOutsiderSessionIntact(t)

	_, err = server.AssignRole(caller, &gen.AssignRoleRequest{
		SubjectId: w.otherTeam, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM, RoleId: w.role, OrgId: w.org,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a team of another organization is refused")

	assignments, err := testService.ListRoleAssignments(testCtx, &gen.ListRoleAssignmentsRequest{OrgId: w.org, SubjectId: w.outsider})
	require.NoError(t, err)
	require.Empty(t, assignments.GetAssignments(), "no refused request wrote an assignment")

	_, err = server.AssignRole(caller, &gen.AssignRoleRequest{
		SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role, OrgId: w.org,
	})
	require.NoError(t, err, "a member of the organization is admitted")
}

// The platform-level path names no organization and is authorised separately;
// the organization check does not apply to it.
func TestAssignRoleKeepsThePlatformLevelPath(t *testing.T) {
	w := newTenancyWorld(t)
	var global string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		roles, err := testStore.ListRoles(ctx, "")
		for _, role := range roles {
			if role.GetBuiltIn() && role.GetOrgId() == "" {
				global = role.GetId()
				break
			}
		}
		return err
	}))
	require.NotEmpty(t, global, "a built-in role exists")
	_, err := testService.AssignRole(testCtx, &gen.AssignRoleRequest{
		SubjectId: w.outsider, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: global,
	})
	require.NoError(t, err)
}

func TestShareRecordRequiresTheSubjectToBelongToTheOrganization(t *testing.T) {
	w := newTenancyWorld(t)
	server := &adapters.PermServer{}
	caller := storyCallerContext(testCtx, w.admin, w.org)
	share := func(subject string, kind gen.SubjectKind) error {
		_, err := server.ShareRecord(caller, &gen.ShareRecordRequest{
			OrgId: w.org, ResourceType: "doc", ResourceId: "record-1",
			SubjectId: subject, SubjectKind: kind, RoleId: w.role,
		})
		return err
	}

	require.Equal(t, codes.PermissionDenied, status.Code(share(w.outsider, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL)))
	require.Equal(t, codes.PermissionDenied, status.Code(share(w.otherTeam, gen.SubjectKind_SUBJECT_KIND_TEAM)))
	require.Equal(t, codes.PermissionDenied, status.Code(share(business.NewIDString(), gen.SubjectKind_SUBJECT_KIND_PRINCIPAL)),
		"a subject that does not exist is refused the same way")

	shares, err := testService.ListShares(testCtx, &gen.ListSharesRequest{OrgId: w.org, ResourceType: "doc", ResourceId: "record-1"})
	require.NoError(t, err)
	require.Empty(t, shares.GetShares(), "no refused request wrote a share")

	require.NoError(t, share(w.member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL), "a member of the organization is admitted")
	w.requireOutsiderSessionIntact(t)
}

// The check holds the membership lock a removal takes, so a removal that
// commits first leaves the waiting write nothing to admit.
func TestSubjectWritesWaitForAConcurrentMembershipRemoval(t *testing.T) {
	writes := map[string]func(w *tenancyWorld) error{
		"AssignRole": func(w *tenancyWorld) error {
			_, err := testService.AssignRole(testCtx, &gen.AssignRoleRequest{
				SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role, OrgId: w.org,
			})
			return err
		},
		"ShareRecord": func(w *tenancyWorld) error {
			_, err := testService.ShareRecord(testCtx, w.admin, &gen.ShareRecordRequest{
				OrgId: w.org, ResourceType: "doc", ResourceId: "record-1",
				SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role,
			})
			return err
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			w := newTenancyWorld(t)
			done := make(chan error, 1)
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				if err := testStore.LockOrgMembership(ctx, w.org, w.member); err != nil {
					return err
				}
				if err := testStore.RemoveOrgMember(ctx, w.org, w.member); err != nil {
					return err
				}
				go func() { done <- write(w) }()
				awaitBlockedTransaction(t, "the write should wait on the membership lock the removal holds")
				select {
				case err := <-done:
					t.Fatalf("the write finished while the removal held the membership: %v", err)
				default:
				}
				return nil
			}))
			err := <-done
			require.True(t, errors.Is(err, business.ErrSubjectOutsideOrganization), "got %v", err)
		})
	}
}

// Match a waiter against an advisory lock held by this exact transaction.
// Waiting on an authorization-revision row is the bug, not proof of exclusion.
func awaitSubjectMembershipWait(t *testing.T, holder context.Context) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := storetx.Tx(holder).QueryRow(holder, `
			SELECT EXISTS (
				SELECT 1 FROM pg_locks waiter JOIN pg_locks held
				USING (locktype, database, classid, objid, objsubid)
				WHERE NOT waiter.granted AND held.granted
				  AND held.locktype = 'advisory' AND held.pid = pg_backend_pid()
			)`).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 20*time.Millisecond, "the contender must wait on this transaction's membership lock")
}

func writeTenancySubject(ctx context.Context, svc *business.Service, w *tenancyWorld, write, org, subject string) error {
	if write == "AssignRole" {
		_, err := svc.AssignRole(ctx, &gen.AssignRoleRequest{
			SubjectId: subject, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId: w.role, OrgId: org, Scope: "docs",
		})
		return err
	}
	_, err := svc.ShareRecord(ctx, w.admin, &gen.ShareRecordRequest{
		OrgId: org, ResourceType: "doc", ResourceId: "record-1",
		SubjectId: subject, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role,
	})
	return err
}

func (w *tenancyWorld) requireNoSubjectWrite(t *testing.T) {
	t.Helper()
	assignments, err := testService.ListRoleAssignments(testCtx, &gen.ListRoleAssignmentsRequest{OrgId: w.org, SubjectId: w.member})
	require.NoError(t, err)
	for _, assignment := range assignments.Assignments {
		require.NotEqual(t, w.role, assignment.RoleId, "no assignment may land after removal")
	}
	shares, err := testService.ListShares(testCtx, &gen.ListSharesRequest{OrgId: w.org, ResourceType: "doc", ResourceId: "record-1"})
	require.NoError(t, err)
	require.Empty(t, shares.Shares, "no share may land after removal")
}

func TestSubjectWritesWaitForNonCanonicalMembershipRemoval(t *testing.T) {
	for name, spell := range map[string]func(string) string{
		"uppercase": strings.ToUpper,
		"compact":   func(id string) string { return strings.ReplaceAll(id, "-", "") },
		"braced":    func(id string) string { return "{" + id + "}" },
		"urn":       func(id string) string { return "urn:uuid:" + id },
	} {
		for _, spellingOnRemoval := range []bool{false, true} {
			for _, write := range []string{"AssignRole", "ShareRecord"} {
				which := "write"
				if spellingOnRemoval {
					which = "removal lock"
				}
				t.Run(name+"/"+which+"/"+write, func(t *testing.T) {
					w := newTenancyWorld(t)
					ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
					var wg sync.WaitGroup
					t.Cleanup(func() { cancel(); wg.Wait() })
					done := make(chan error, 1)
					require.NoError(t, testStore.WithControlPlane(ctx, func(txctx context.Context) error {
						lockOrg, lockUser, writeOrg, writeUser := w.org, w.member, spell(w.org), spell(w.member)
						if spellingOnRemoval {
							lockOrg, lockUser, writeOrg, writeUser = spell(w.org), spell(w.member), w.org, w.member
						}
						if err := testStore.LockOrgMembership(txctx, lockOrg, lockUser); err != nil {
							return err
						}
						if err := testStore.RemoveOrgMember(txctx, w.org, w.member); err != nil {
							return err
						}
						wg.Add(1)
						go func() { defer wg.Done(); done <- writeTenancySubject(ctx, testService, w, write, writeOrg, writeUser) }()
						awaitSubjectMembershipWait(t, txctx)
						return nil
					}))
					require.ErrorIs(t, <-done, business.ErrSubjectOutsideOrganization)
					w.requireNoSubjectWrite(t)
				})
			}
		}
	}
}

// Pause after the real bulk DELETE while its transaction and locks remain open.
type pausedBulkMembershipStore struct {
	*infra.PostgresStore
	removed chan context.Context
	release <-chan struct{}
}

func (s *pausedBulkMembershipStore) RemoveAllOrgMembers(ctx context.Context, org string) (int64, error) {
	n, err := s.PostgresStore.RemoveAllOrgMembers(ctx, org)
	return s.pauseAfterRemoval(ctx, n, err)
}

func (s *pausedBulkMembershipStore) RemoveLockedOrgMembers(ctx context.Context, org string, members []string) (int64, error) {
	n, err := s.PostgresStore.RemoveLockedOrgMembers(ctx, org, members)
	return s.pauseAfterRemoval(ctx, n, err)
}

func (s *pausedBulkMembershipStore) pauseAfterRemoval(ctx context.Context, n int64, err error) (int64, error) {
	if err != nil {
		return n, err
	}
	select {
	case s.removed <- ctx:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case <-s.release:
		return n, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestSubjectWritesWaitForBulkMembershipRemoval(t *testing.T) {
	for _, deletion := range []string{"RemoveAllOrgMembers", "DeleteOrganization"} {
		for _, write := range []string{"AssignRole", "ShareRecord"} {
			t.Run(deletion+"/"+write, func(t *testing.T) {
				w := newTenancyWorld(t)
				ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
				release := make(chan struct{})
				store := &pausedBulkMembershipStore{PostgresStore: testStore, removed: make(chan context.Context, 1), release: release}
				writer, err := business.NewService(testStore)
				require.NoError(t, err)
				var wg sync.WaitGroup
				t.Cleanup(func() { cancel(); wg.Wait(); testService.SetStore(testStore) })
				testService.SetStore(store)
				removed := make(chan error, 1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					if deletion == "DeleteOrganization" {
						removed <- testService.DeleteOrganization(ctx, w.admin, &gen.DeleteOrganizationRequest{OrgId: w.org, ConfirmSlug: "subject-a-admin-org"})
					} else {
						removed <- testStore.WithControlPlane(ctx, func(txctx context.Context) error { _, err := store.RemoveAllOrgMembers(txctx, w.org); return err })
					}
				}()
				var holder context.Context
				select {
				case holder = <-store.removed:
				case err := <-removed:
					t.Fatalf("removal failed before barrier: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				written := make(chan error, 1)
				wg.Add(1)
				go func() { defer wg.Done(); written <- writeTenancySubject(ctx, writer, w, write, w.org, w.member) }()
				awaitSubjectMembershipWait(t, holder)
				close(release)
				require.NoError(t, <-removed)
				require.ErrorIs(t, <-written, business.ErrSubjectOutsideOrganization)
				w.requireNoSubjectWrite(t)
			})
		}
	}
}

// Pause an actual AssignRole after its membership check and before the write
// acquires authorization-revision/session locks. Archiving must wait here.
type pausedSubjectAssignmentStore struct {
	*infra.PostgresStore
	checked chan context.Context
	release <-chan struct{}
}

func (s *pausedSubjectAssignmentStore) AssignRole(ctx context.Context, assignment *gen.RoleAssignment) error {
	select {
	case s.checked <- ctx:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return s.PostgresStore.AssignRole(ctx, assignment)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestDeleteOrganizationWaitsForSubjectAssignmentBeforeTriggerWrites(t *testing.T) {
	w := newTenancyWorld(t)
	hash := sha256.Sum256([]byte("member-refresh-" + w.member))
	now := time.Now()
	require.NoError(t, testSessionStore.Insert(testCtx, &authcore.SessionRecord{
		UserID: uuid.MustParse(w.member), OrgID: uuid.MustParse(w.org), FamilyID: uuid.New(), RefreshHash: hash[:],
		AuthenticatedAt: now, IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
	}))
	ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
	release := make(chan struct{})
	store := &pausedSubjectAssignmentStore{PostgresStore: testStore, checked: make(chan context.Context, 1), release: release}
	writer, err := business.NewService(store)
	require.NoError(t, err)
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	written := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); written <- writeTenancySubject(ctx, writer, w, "AssignRole", w.org, w.member) }()
	var holder context.Context
	select {
	case holder = <-store.checked:
	case err := <-written:
		t.Fatalf("assignment failed before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	removed := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		removed <- testService.DeleteOrganization(ctx, w.admin, &gen.DeleteOrganizationRequest{OrgId: w.org, ConfirmSlug: "subject-a-admin-org"})
	}()
	awaitSubjectMembershipWait(t, holder)
	close(release)
	require.NoError(t, <-written, "the earlier assignment must commit without a trigger-lock deadlock")
	require.NoError(t, <-removed)
	require.False(t, isOrgMember(t, testCtx, w.org, w.member))
	require.ErrorIs(t, writeTenancySubject(ctx, writer, w, "AssignRole", w.org, w.member), business.ErrSubjectOutsideOrganization)
}
