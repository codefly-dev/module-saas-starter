//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// UpdateRole replaces a role's whole permission set as DELETE-then-INSERT. Under
// READ COMMITTED a second editor's DELETE takes its snapshot before the first
// editor commits, so it never sees the rows the first one inserted: it deletes
// what it read (already gone), inserts its own set, and the role is left holding
// the UNION of two sets neither administrator asked for — strictly more
// authority than either save requested.
//
// What prevents it is that the two editors serialize on the role row, and the
// observable consequence is that the second BLOCKS until the first commits.
// This pins that property rather than the mechanism: today both the FOR UPDATE
// in the lookup and the unconditional description write take that lock, and
// removing either one alone still passes. It fails the moment the role stops
// being locked for the whole replace — which is what a "only write the
// description when it changed" optimization would do.
//
// A plain goroutine race does not reproduce this (the two transactions do not
// interleave in the dangerous window on their own), so the first transaction is
// held open deliberately and the second must not get through it.
func TestUpdateRoleSerializesConcurrentEditorsOnTheRole(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id:    roleID,
			Name:  "contended " + roleID,
			OrgId: orgID,
			Permissions: []*gen.Permission{
				{Resource: "users", Action: "read"},
				{Resource: "users", Action: "write"},
			},
		})
	}))

	firstHasLock := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			if _, err := testStore.UpdateRole(ctx, roleID, orgID, "first",
				[]*gen.Permission{{Resource: "users", Action: "read"}}); err != nil {
				return err
			}
			close(firstHasLock)
			<-releaseFirst
			return nil
		})
	}()
	<-firstHasLock

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			_, err := testStore.UpdateRole(ctx, roleID, orgID, "second",
				[]*gen.Permission{{Resource: "users", Action: "write"}})
			return err
		})
	}()

	select {
	case err := <-secondDone:
		t.Fatalf("the second editor completed while the first still held the role (err=%v): "+
			"its DELETE cannot see the first editor's inserts, so the sets union", err)
	case <-time.After(750 * time.Millisecond):
	}

	close(releaseFirst)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)

	var stored []*gen.Permission
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		roles, err := testStore.ListRoles(ctx, orgID)
		if err != nil {
			return err
		}
		for _, role := range roles {
			if role.Id == roleID {
				stored = role.Permissions
			}
		}
		return nil
	}))
	require.Len(t, stored, 1, "one save must win whole; a union means neither intent was applied")
	require.Equal(t, "write", stored[0].Action)
}

// The store must also refuse to describe a role by the request rather than by
// what it wrote: ON CONFLICT DO NOTHING collapses a duplicate grant.
func TestUpdateRoleReturnsTheStoredPermissionSet(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleID, Name: "dedupe " + roleID, OrgId: orgID,
		})
	}))

	var updated *gen.Role
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		role, err := testStore.UpdateRole(ctx, roleID, orgID, "", []*gen.Permission{
			{Resource: "users", Action: "read"},
			{Resource: "users", Action: "read"},
		})
		updated = role
		return err
	}))
	require.Len(t, updated.Permissions, 1)
}
