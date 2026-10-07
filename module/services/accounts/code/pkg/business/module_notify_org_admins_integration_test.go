//go:build !pure

package business_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Against Postgres, under the codefly harness: NotifyOrgAdmins resolves the
// tenant's administrators from the real membership table, writes one inbox row
// to each, and none to a plain member; a caller bound to another tenant is
// refused and writes nothing.
func TestModuleNotifyOrgAdmins_ReachesOnlyTheTenantsAdministrators(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@notify-admins.test", "owner-notify-admins", "Acme Notify Admins")
	member := mustMember(t, owner, org, "member@notify-admins.test", "member-notify-admins")
	admin := mustUser(t, "admin@notify-admins.test", "admin-notify-admins")
	require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: admin, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
	_, otherOrg := mustUserAndOrg(t, testCtx, "owner@notify-admins-b.test", "owner-notify-admins-b", "Acme Notify Admins B")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	backend := &fakeJobBackend{}
	svc.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{modulePrincSvc: {}})
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: org}

	delivered, err := svc.ModuleNotifyOrgAdmins(testCtx, caller, business.ModuleNotifyOrgAdminsInput{
		Tenant: org, Title: "A delegation was revoked", Body: "Reconnect the source.",
		Type: "warning", Category: "security", IdempotencyKey: "delegation-revoked-1",
	})
	require.NoError(t, err)
	require.True(t, delivered)

	// Only this call's rows are counted: a person's inbox also carries what the
	// rest of the host sends them (membership changes reach an owner, for one),
	// which is not this capability's to assert on. A second row from this call
	// would still be counted, and fail.
	const title = "A delegation was revoked"
	inbox := func(user string) []*business.Notification {
		t.Helper()
		rows, _, err := testService.ListNotifications(testCtx, user, 50, "")
		require.NoError(t, err)
		var mine []*business.Notification
		var others []string
		for _, row := range rows {
			if row.Title == title {
				mine = append(mine, row)
			} else {
				others = append(others, row.Type+": "+row.Title)
			}
		}
		t.Logf("%s also holds %d unrelated notification(s): %v", user, len(others), others)
		return mine
	}
	for _, administrator := range []string{owner, admin} {
		rows := inbox(administrator)
		require.Len(t, rows, 1, "administrator %s", administrator)
		require.Equal(t, "warning", rows[0].Type)
		require.Equal(t, org, rows[0].OrgID)
	}
	require.Empty(t, inbox(member), "a member who is not an administrator is not notified")

	// A redelivery — the same notification under the same key — converges on
	// the rows already written.
	_, err = svc.ModuleNotifyOrgAdmins(testCtx, caller, business.ModuleNotifyOrgAdminsInput{
		Tenant: org, Title: title, Body: "Reconnect the source.",
		Type: "warning", Category: "security", IdempotencyKey: "delegation-revoked-1",
	})
	require.NoError(t, err)
	require.Len(t, inbox(owner), 1, "the idempotency key dedupes a redelivery")

	// A different notification under a key already used is the caller's error,
	// not a storage failure, and changes nothing.
	_, err = svc.ModuleNotifyOrgAdmins(testCtx, caller, business.ModuleNotifyOrgAdminsInput{
		Tenant: org, Title: title, Body: "A different body.",
		Type: "warning", Category: "security", IdempotencyKey: "delegation-revoked-1",
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "err = %v", err)
	require.Equal(t, "Reconnect the source.", inbox(owner)[0].Body)

	_, err = svc.ModuleNotifyOrgAdmins(testCtx, caller, business.ModuleNotifyOrgAdminsInput{
		Tenant: otherOrg, Title: "cross-tenant", Type: "info", Category: "security",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.ModuleNotifyOrgAdmins(testCtx, caller, business.ModuleNotifyOrgAdminsInput{
		Tenant: org, Title: "bad type", Type: "sync", Category: "security",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Len(t, inbox(owner), 1, "a refused call writes nothing")
}

// Against Postgres: a same-origin path a module supplies is stored and handed
// back verbatim when the recipient follows it, and an off-origin destination is
// refused with nothing written.
func TestModuleNotifyUser_StoresAndResolvesASameOriginDestination(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@notify-action.test", "owner-notify-action", "Acme Notify Action")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	backend := &fakeJobBackend{}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{modulePrincSvc: {}})
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: org}

	const destination = "/documents/doc-7/comments/3?page=2#reply"
	result, err := svc.ModuleNotifyUser(testCtx, caller, business.ModuleNotifyUserInput{
		Tenant: org, UserID: owner, Title: "A document needs you", Body: "Reply to the comment.",
		Type: "info", Category: "security", ActionURL: destination,
	})
	require.NoError(t, err)
	require.True(t, result.Delivered)

	followed, err := testService.ResolveNotificationAction(testCtx, owner, result.NotificationID)
	require.NoError(t, err)
	require.Equal(t, destination, followed)

	_, err = svc.ModuleNotifyUser(testCtx, caller, business.ModuleNotifyUserInput{
		Tenant: org, UserID: owner, Title: "A document needs you", Body: "Reply to the comment.",
		Type: "info", Category: "security", ActionURL: "//evil.example/steal",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "err = %v", err)

	rows, _, err := testService.ListNotifications(testCtx, owner, 50, "")
	require.NoError(t, err)
	var destinations []string
	for _, row := range rows {
		if row.ActionURL != "" {
			destinations = append(destinations, row.ActionURL)
		}
	}
	require.Equal(t, []string{destination}, destinations, "a refused destination reached the inbox")
}
