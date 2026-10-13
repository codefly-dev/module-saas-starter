//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
)

// The old FOR SHARE OF i, t query locked the installation before the target.
// With withdrawal already holding the target, its installation lock completed
// a cycle and PostgreSQL aborted one transaction with 40P01. Observe the wait
// before letting withdrawal continue, so that schedule is mandatory.
func TestModuleAuthoritySerializesWithTargetWithdrawal(t *testing.T) {
	for _, checkFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "capability_first", false: "withdrawal_first"}[checkFirst], func(t *testing.T) {
			a := newDynamicInstall(t, "", "", "")
			ctx, cancel := context.WithTimeout(testCtx, 30*time.Second)
			var workers sync.WaitGroup
			t.Cleanup(func() { cancel(); workers.Wait() })

			// These are the same store calls, in the same transaction, used by
			// reconcileSolutionTarget after GetLiveSolutionTargetForUpdate.
			finishWithdrawal := func(ctx context.Context, target *business.SolutionTarget) error {
				if target == nil || target.ID != a.target {
					return fmt.Errorf("withdrawal did not lock the installed target: %+v", target)
				}
				now := time.Now().UTC()
				if err := testStore.CloseSolutionTarget(ctx, target.ID, 2, now); err != nil {
					return err
				}
				revoked, err := testStore.RevokeInstallationsOfTarget(ctx, target.ID, "solution target was withdrawn", now)
				if err != nil {
					return err
				}
				if len(revoked) != 1 || revoked[0].InstallationID != a.installation.Id || revoked[0].OrgID != a.org {
					return fmt.Errorf("withdrawal did not revoke the expected installation: %+v", revoked)
				}
				return nil
			}
			receive := func(done <-chan error) error {
				select {
				case err := <-done:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			var checkErr, withdrawalErr error
			if checkFirst {
				useCtx, release, err := testStore.BeginModuleAuthorityUse(ctx)
				require.NoError(t, err)
				release = sync.OnceFunc(release)
				defer release()
				live, err := testStore.LiveModuleAuthority(useCtx, a.principal, a.org, "")
				require.NoError(t, err)
				require.Equal(t, a.installation.Id, live.InstallationID)

				started, withdrawn := make(chan uint32, 1), make(chan error, 1)
				workers.Add(1)
				go func() {
					defer workers.Done()
					withdrawn <- testStore.WithControlPlane(ctx, func(ctx context.Context) error {
						started <- storetx.Tx(ctx).Conn().PgConn().PID()
						target, err := testStore.GetLiveSolutionTargetForUpdate(ctx, a.binding)
						if err != nil {
							return err
						}
						return finishWithdrawal(ctx, target)
					})
				}()
				var withdrawalPID uint32
				select {
				case withdrawalPID = <-started:
				case err := <-withdrawn:
					t.Fatalf("withdrawal did not start: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				require.Eventually(t, func() bool {
					var blocked bool
					err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1::int)) > 0`, withdrawalPID).Scan(&blocked)
					return err == nil && blocked
				}, 10*time.Second, 20*time.Millisecond, "withdrawal must wait for the capability's target lock")
				select {
				case err := <-withdrawn:
					t.Fatalf("withdrawal finished while capability locks were held: %v", err)
				default:
				}
				release() // End the real read-only authority transaction before withdrawal proceeds.
				withdrawalErr = receive(withdrawn)
			} else {
				checked := make(chan error, 1)
				withdrawalErr = testStore.WithControlPlane(ctx, func(withdrawCtx context.Context) error {
					target, err := testStore.GetLiveSolutionTargetForUpdate(withdrawCtx, a.binding)
					if err != nil {
						return err
					}
					require.NotNil(t, target)
					require.Equal(t, a.target, target.ID)
					workers.Add(1)
					go func() {
						defer workers.Done()
						_, err := testStore.LiveModuleAuthority(ctx, a.principal, a.org, "")
						checked <- err
					}()
					// Only this fresh target is locked by withdrawal so far. Match
					// its backend as the blocker, not an unrelated database waiter.
					require.Eventually(t, func() bool {
						var blocked bool
						err := storetx.Tx(withdrawCtx).QueryRow(withdrawCtx, `
							SELECT EXISTS (SELECT 1 FROM pg_locks
							 WHERE NOT granted AND pg_backend_pid() = ANY(pg_blocking_pids(pid)))`).Scan(&blocked)
						return err == nil && blocked
					}, 10*time.Second, 20*time.Millisecond, "capability check must wait on withdrawal's target lock")
					select {
					case err := <-checked:
						t.Fatalf("capability check finished while withdrawal held the target: %v", err)
					default:
					}
					// Under the old query the reader now holds I and waits for T;
					// continuing to lock I here deterministically closes the cycle.
					return finishWithdrawal(withdrawCtx, target)
				})
				checkErr = receive(checked)
			}
			require.NoError(t, withdrawalErr, "withdrawal must commit without 40P01")
			if !checkFirst {
				require.ErrorIs(t, checkErr, business.ErrModuleInstallationInactive, "the waiting check must refuse committed withdrawal, not fail with 40P01")
			}

			require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
				target, err := testStore.GetSolutionTarget(ctx, a.target)
				if err != nil {
					return err
				}
				require.NotNil(t, target)
				require.NotNil(t, target.ClosedGeneration)
				require.Equal(t, uint64(2), *target.ClosedGeneration)
				installation, _, err := testStore.GetInstallation(ctx, a.org, a.installation.Id)
				if err != nil {
					return err
				}
				require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, installation.Status)
				return nil
			}))
			_, err := testStore.LiveModuleAuthority(ctx, a.principal, a.org, "")
			require.ErrorIs(t, err, business.ErrModuleInstallationInactive, "subsequent capability checks must see the committed withdrawal")
		})
	}
}
