package infra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"accounts/pkg/infra/internal/txbind"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

const lockTestOrg = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
const lockTestUser = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"

// Exercise the production lock and removal methods on a transaction double;
// PostgreSQL contention and triggers are covered by the business DB tests.
type membershipTx struct {
	pgx.Tx
	exec  func(context.Context, string, ...any) (pgconn.CommandTag, error)
	query func(context.Context, string, ...any) (pgx.Rows, error)
}

func (tx *membershipTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return tx.exec(ctx, sql, args...)
}

func (tx *membershipTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return tx.query(ctx, sql, args...)
}

type membershipRows struct {
	pgx.Rows
	ids    []string
	i      int
	closed bool
}

func (r *membershipRows) Next() bool             { r.i++; return r.i <= len(r.ids) }
func (r *membershipRows) Scan(dest ...any) error { *dest[0].(*string) = r.ids[r.i-1]; return nil }
func (r *membershipRows) Close()                 { r.closed = true }
func (r *membershipRows) Err() error             { return nil }

func uuidLockSpellings(id string) map[string]string {
	return map[string]string{
		"canonical": id, "uppercase": strings.ToUpper(id),
		"compact": strings.ReplaceAll(id, "-", ""), "braced": "{" + id + "}",
		"urn": "urn:uuid:" + id,
	}
}

// A removal already holds the canonical lock. Every spelling of both UUIDs
// must contend on that same key; a second key lets the subject check pass the
// still-visible membership while the removal's transaction remains open.
func TestMembershipLockUUIDSpellingsWaitForRemoval(t *testing.T) {
	store := &PostgresStore{}
	var removalKey string
	removal := &membershipTx{exec: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
		removalKey = args[0].(string)
		return pgconn.CommandTag{}, nil
	}}
	require.NoError(t, store.LockOrgMembership(txbind.BindControlPlane(context.Background(), removal), lockTestOrg, lockTestUser))
	for orgName, org := range uuidLockSpellings(lockTestOrg) {
		for userName, user := range uuidLockSpellings(lockTestUser) {
			t.Run(orgName+"/"+userName, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				waiting := make(chan struct{})
				waiter := &membershipTx{exec: func(ctx context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
					if args[0] == removalKey {
						close(waiting)
						<-ctx.Done()
						return pgconn.CommandTag{}, ctx.Err()
					}
					return pgconn.CommandTag{}, nil
				}}
				done := make(chan error, 1)
				go func() { done <- store.LockOrgMembership(txbind.BindControlPlane(ctx, waiter), org, user) }()
				select {
				case <-waiting:
					cancel()
					require.ErrorIs(t, <-done, context.Canceled)
				case err := <-done:
					t.Fatalf("alternate spelling bypassed the removal lock: %v", err)
				case <-ctx.Done():
					t.Fatal("lock never attempted")
				}
			})
		}
	}
}

func TestMembershipLockRejectsMalformedUUIDsBeforeSQL(t *testing.T) {
	store := &PostgresStore{}
	tx := &membershipTx{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		t.Fatal("malformed UUID reached SQL")
		return pgconn.CommandTag{}, nil
	}}
	ctx := txbind.BindControlPlane(context.Background(), tx)
	for _, invalid := range []string{"", "not-a-uuid"} {
		require.Error(t, store.LockOrgMembership(ctx, invalid, lockTestUser))
		require.Error(t, store.LockOrgMembership(ctx, lockTestOrg, invalid))
		require.Error(t, store.LockOrgAdministration(ctx, invalid))
	}
}

func TestBulkMembershipRemovalLocksBeforeDelete(t *testing.T) {
	store := &PostgresStore{}
	second := "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa"
	for _, failLock := range []bool{false, true} {
		t.Run(map[bool]string{false: "assignment waits", true: "lock failure refuses removal"}[failLock], func(t *testing.T) {
			var calls []string
			locked := map[string]bool{}
			rows := &membershipRows{ids: []string{second, lockTestUser}}
			lockErr := errors.New("membership lock refused")
			tx := &membershipTx{}
			tx.query = func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
				require.Contains(t, sql, "SELECT user_id FROM organization_members")
				require.Equal(t, []any{lockTestOrg}, args)
				calls = append(calls, "roster")
				return rows, nil
			}
			tx.exec = func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				if strings.Contains(sql, "pg_advisory_xact_lock") {
					key := args[0].(string)
					calls = append(calls, key)
					if strings.HasPrefix(key, "membership:") {
						require.True(t, rows.closed, "release query rows before acquiring locks on this connection")
						if failLock && strings.HasSuffix(key, second) {
							return pgconn.CommandTag{}, lockErr
						}
					}
					locked[key] = true
					return pgconn.CommandTag{}, nil
				}
				calls = append(calls, "delete")
				require.Contains(t, sql, "user_id = ANY($2::uuid[])", "never delete a member outside the locked roster")
				require.Equal(t, []any{lockTestOrg, []string{lockTestUser, second}}, args)
				return pgconn.NewCommandTag("DELETE 2"), nil
			}
			count, err := store.RemoveAllOrgMembers(txbind.BindControlPlane(context.Background(), tx), strings.ToUpper(lockTestOrg))
			want := []string{"administration:" + lockTestOrg, "roster", "membership:" + lockTestOrg + ":" + lockTestUser, "membership:" + lockTestOrg + ":" + second}
			if failLock {
				require.ErrorIs(t, err, lockErr)
				require.Zero(t, count)
				require.Equal(t, want, calls, "no delete after a failed lock")
				return
			}
			require.NoError(t, err)
			require.EqualValues(t, 2, count)
			require.Equal(t, append(want, "delete"), calls, "take all locks in sorted order before the first delete")

			// The bulk transaction is still open: the production membership
			// lock used by an assignment must wait behind it for either user.
			for _, user := range []string{lockTestUser, second} {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				waiting := make(chan struct{})
				waiter := &membershipTx{exec: func(ctx context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
					if locked[args[0].(string)] {
						close(waiting)
						<-ctx.Done()
						return pgconn.CommandTag{}, ctx.Err()
					}
					return pgconn.CommandTag{}, nil
				}}
				done := make(chan error, 1)
				go func() { done <- store.LockOrgMembership(txbind.BindControlPlane(ctx, waiter), lockTestOrg, user) }()
				select {
				case <-waiting:
					cancel()
					require.ErrorIs(t, <-done, context.Canceled)
				case err := <-done:
					t.Fatalf("assignment bypassed the bulk removal lock: %v", err)
				case <-ctx.Done():
					t.Fatal("assignment never attempted its membership lock")
				}
			}

		})
	}
}

func TestBulkMembershipRemovalRequiresTransactionAndValidOrg(t *testing.T) {
	store := &PostgresStore{}
	_, err := store.RemoveAllOrgMembers(context.Background(), lockTestOrg)
	require.ErrorContains(t, err, "requires a tenant transaction")
	_, err = store.RemoveAllOrgMembers(context.Background(), "not-a-uuid")
	require.Error(t, err)
}
