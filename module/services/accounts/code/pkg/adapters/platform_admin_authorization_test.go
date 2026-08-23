package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

const (
	platformActorID = "019f6bf7-5b1c-730d-9687-fe6d4aff31ee"
	platformTargetD = "019f6bf7-5b4b-74e5-8c17-092259bb1663"
)

// platformAdminAuthzStore implements only the methods the authorization path
// touches: GetPlatformRole for the gate, and WithUserTx/HasVerifiedMFA so a test
// can observe whether MFA enrollment was probed (proving ImpersonateUser denies
// a non-admin before touching MFA state). Every other data-access method is left
// to the embedded nil interface, so a handler that fell through to business
// logic after the role gate should have denied the caller panics instead of
// passing silently.
type platformAdminAuthzStore struct {
	business.Store
	role      string
	mfaProbed bool
}

func (f *platformAdminAuthzStore) GetPlatformRole(context.Context, string) (string, error) {
	return f.role, nil
}

func (f *platformAdminAuthzStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	f.mfaProbed = true
	return fn(ctx)
}

func (f *platformAdminAuthzStore) HasVerifiedMFA(context.Context, string) (bool, error) {
	f.mfaProbed = true
	return true, nil
}

// platformAdminHandler names one of the 14 PlatformAdminService methods and the
// minimum platform role its business layer enforces, so the handler-layer gate
// can be checked against the same bar.
type platformAdminHandler struct {
	name    string
	minRole string
	invoke  func(ctx context.Context, srv *PlatformAdminServer) error
}

func platformAdminHandlers() []platformAdminHandler {
	return []platformAdminHandler{
		{"SearchUsers", "support", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.SearchUsers(ctx, &gen.SearchUsersRequest{Query: "x", PageSize: 10})
			return err
		}},
		{"SuspendUser", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.SuspendUser(ctx, &gen.SuspendUserRequest{UserId: platformTargetD, Reason: "test"})
			return err
		}},
		{"UnsuspendUser", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.UnsuspendUser(ctx, &gen.UnsuspendUserRequest{UserId: platformTargetD})
			return err
		}},
		{"ImpersonateUser", "support", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.ImpersonateUser(ctx, &gen.ImpersonateUserRequest{UserId: platformTargetD})
			return err
		}},
		{"ListActiveSessions", "support", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.ListActiveSessions(ctx, &gen.ListActiveSessionsRequest{UserId: platformTargetD, PageSize: 10})
			return err
		}},
		{"RevokeSession", "support", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.RevokeSession(ctx, &gen.RevokeSessionRequest{SessionId: platformTargetD, Reason: "test"})
			return err
		}},
		{"GrantPlatformRole", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.GrantPlatformRole(ctx, &gen.GrantPlatformRoleRequest{UserId: platformTargetD, PlatformRole: "support"})
			return err
		}},
		{"RevokePlatformRole", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.RevokePlatformRole(ctx, &gen.RevokePlatformRoleRequest{UserId: platformTargetD})
			return err
		}},
		{"ListPlatformAdmins", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.ListPlatformAdmins(ctx, &gen.ListPlatformAdminsRequest{})
			return err
		}},
		{"ListFeatureFlags", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.ListFeatureFlags(ctx, &gen.ListFeatureFlagsRequest{})
			return err
		}},
		{"GetJobOperations", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.GetJobOperations(ctx, &jobsv1.GetJobOperationsRequest{})
			return err
		}},
		{"ListJobs", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.ListJobs(ctx, &jobsv1.ListJobsRequest{PageSize: 10})
			return err
		}},
		{"GetJob", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.GetJob(ctx, &jobsv1.GetJobRequest{JobId: platformTargetD})
			return err
		}},
		{"ReplayJob", "super_admin", func(ctx context.Context, srv *PlatformAdminServer) error {
			_, err := srv.ReplayJob(ctx, &jobsv1.ReplayJobRequest{SourceJobId: platformTargetD, IdempotencyKey: "replay-key"})
			return err
		}},
	}
}

// A caller with no platform role must be denied by every platform-admin handler
// before the request ever reaches the business layer — the handler gate stands
// on its own, so dropping the sibling business-layer check cannot re-expose the
// endpoint.
func TestPlatformAdminHandlersRejectNonAdmin(t *testing.T) {
	for _, h := range platformAdminHandlers() {
		t.Run(h.name, func(t *testing.T) {
			installLayeredAuthzService(t, &platformAdminAuthzStore{role: ""})

			ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
			err := h.invoke(ctx, &PlatformAdminServer{})
			require.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

// The handler gate enforces the same minimum role as its business method, so a
// lower-privileged platform admin is denied at the handler for super_admin-only
// endpoints even if the business-layer check were removed.
func TestPlatformAdminHandlersEnforceMinimumRole(t *testing.T) {
	for _, h := range platformAdminHandlers() {
		if h.minRole != "super_admin" {
			continue
		}
		t.Run(h.name, func(t *testing.T) {
			installLayeredAuthzService(t, &platformAdminAuthzStore{role: "support"})

			ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
			err := h.invoke(ctx, &PlatformAdminServer{})
			require.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

// ImpersonateUser must deny a non-admin before requireMFA reads their MFA
// enrollment state. The context carries no recent MFA assurance, so an
// out-of-order check would fall through to the store-backed MFA lookup.
func TestImpersonateUserDeniesNonAdminBeforeMFAProbe(t *testing.T) {
	store := &platformAdminAuthzStore{role: ""}
	installLayeredAuthzService(t, store)

	ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
	_, err := (&PlatformAdminServer{}).ImpersonateUser(ctx, &gen.ImpersonateUserRequest{UserId: platformTargetD})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.mfaProbed, "role check must precede the MFA state lookup")
}

func TestRequirePlatformRoleHierarchy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     string
		minRole  string
		wantCode codes.Code
	}{
		{"no role denied", "", "support", codes.PermissionDenied},
		{"support meets support", "support", "support", codes.OK},
		{"support below super_admin", "support", "super_admin", codes.PermissionDenied},
		{"billing below super_admin", "billing", "super_admin", codes.PermissionDenied},
		{"super_admin meets super_admin", "super_admin", "super_admin", codes.OK},
		{"super_admin meets support", "super_admin", "support", codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installLayeredAuthzService(t, &platformAdminAuthzStore{role: tc.role})

			ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
			err := requirePlatformRole(ctx, platformActorID, tc.minRole)
			require.Equal(t, tc.wantCode, status.Code(err))
		})
	}
}
