package business

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Platform role hierarchy for handler authorization checks. Transport admission
// comes from the shared RPC policy; resource-sensitive role checks stay here.
var platformRoleLevel = map[string]int{
	"super_admin": 3,
	"billing":     2,
	"support":     1,
}

// PlatformRoleRank returns the privilege level of a platform role, higher being
// more privileged; an unknown or empty role ranks 0. Handler-layer gates share
// this ordering so the two authorization layers cannot drift.
func PlatformRoleRank(role string) int {
	return platformRoleLevel[role]
}

// requirePlatformRole checks that the actor has at least the given platform role.
// An impersonated request holds no platform authority at all: the effective
// subject's grants are not the actor's to borrow, and the actor's own grants do
// not follow them into someone else's session. Denying here also denies nested
// impersonation, since ImpersonateUser sits behind this gate.
func (s *Service) requirePlatformRole(ctx context.Context, actorID, minRole string) error {
	if auth.ImpersonatedRequest(ctx) {
		return fmt.Errorf("platform authority is unavailable to an impersonated session")
	}
	role, err := s.store.GetPlatformRole(ctx, actorID)
	if err != nil {
		return err
	}
	if role == "" {
		return fmt.Errorf("not a platform admin")
	}
	if PlatformRoleRank(role) < PlatformRoleRank(minRole) {
		return fmt.Errorf("insufficient platform role: have %s, need %s", role, minRole)
	}
	return nil
}

// SearchUsers searches all users across orgs (platform admin only).
func (s *Service) SearchUsers(ctx context.Context, actorID string, req *gen.SearchUsersRequest) (*gen.SearchUsersResponse, error) {
	w := wool.Get(ctx).In("SearchUsers")

	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	var (
		users     []*gen.User
		nextToken string
	)
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		users, nextToken, err = s.store.SearchUsers(ctx, req.Query, req.PageSize, req.PageToken)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "search failed")
	}

	return &gen.SearchUsersResponse{
		Users:         users,
		NextPageToken: nextToken,
	}, nil
}

// SuspendUser suspends a user account (super_admin only).
func (s *Service) SuspendUser(ctx context.Context, actorID string, req *gen.SuspendUserRequest) error {
	w := wool.Get(ctx).In("SuspendUser")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := s.store.UpdateUserStatus(ctx, req.UserId, "suspended"); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventUserSuspended, "user", req.UserId, "")
	}); err != nil {
		return w.Wrapf(err, "cannot suspend user")
	}

	s.notifySlack(ctx, fmt.Sprintf("Security: user %s suspended by %s (reason: %s)", req.UserId, actorID, req.Reason))
	return nil
}

// UnsuspendUser reactivates a suspended user account (super_admin only).
func (s *Service) UnsuspendUser(ctx context.Context, actorID string, req *gen.UnsuspendUserRequest) error {
	w := wool.Get(ctx).In("UnsuspendUser")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := s.store.UpdateUserStatus(ctx, req.UserId, "active"); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventUserUnsuspended, "user", req.UserId, "")
	}); err != nil {
		return w.Wrapf(err, "cannot unsuspend user")
	}
	return nil
}

// ImpersonateUser issues a token whose real actor is the calling platform admin
// and whose effective subject is the target (support+ only). The minted Identity
// keeps the actor as UserID and names the target through ActingAsUserID; every
// transport projects that pair onto auth.RequestIdentity, so downstream
// authorization runs as the target while audit stays attributable to the actor.
//
// The session is deliberately narrow. It carries no platform role, and
// requirePlatformRole denies platform authority to any request already acting as
// someone else — which is also what forbids impersonating from inside an
// impersonated session. The target must be an active account: a suspended or
// deleted user cannot be stepped into, so a support session can never outlive the
// account's own lifecycle. The token TTL is capped independently by the minter.
func (s *Service) ImpersonateUser(ctx context.Context, actorID string, req *gen.ImpersonateUserRequest) (*gen.ImpersonateUserResponse, error) {
	w := wool.Get(ctx).In("ImpersonateUser")

	if s.minter == nil {
		return nil, w.NewError("auth path not wired: minter missing")
	}
	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	// Cross-tenant lookups: a platform admin impersonating any user needs to see
	// the target's account state, orgs and role regardless of the caller's own
	// tenant. WithControlPlane elevates for the reads; the impersonation session
	// that gets minted carries the resolved orgID so downstream tenant-scoped ops
	// run correctly under the target's org.
	var (
		target *gen.User
		orgs   []*gen.Organization
	)
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		t, err := s.store.GetUser(ctx, req.UserId)
		if err != nil {
			return err
		}
		target = t
		os, err := s.store.ListOrganizationsForUser(ctx, req.UserId)
		orgs = os
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot resolve target user")
	}
	if target.GetStatus() != gen.UserStatus_USER_STATUS_ACTIVE {
		return nil, w.NewError("target user is not active")
	}

	// The target's organizations come back ordered by name, so a target in
	// several organizations always yields the same session org rather than
	// whichever row the planner returned first.
	orgID := ""
	orgRole := ""
	if len(orgs) > 0 {
		orgID = orgs[0].Id
		var members []*gen.OrgMembership
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			ms, err := s.store.ListOrgMembers(ctx, orgID)
			members = ms
			return err
		}); err != nil {
			return nil, w.Wrapf(err, "cannot list org members")
		}
		for _, m := range members {
			if m.UserId == req.UserId {
				orgRole = m.Role.String()
				break
			}
		}
	}

	actorUUID, err := ParseID(actorID)
	if err != nil {
		return nil, w.Wrapf(err, "parse actor id")
	}
	targetUUID, err := ParseID(req.UserId)
	if err != nil {
		return nil, w.Wrapf(err, "parse target id")
	}
	var orgUUID uuid.UUID
	if orgID != "" {
		orgUUID, err = ParseID(orgID)
		if err != nil {
			return nil, w.Wrapf(err, "parse org id")
		}
	}

	// PlatformRole is intentionally EMPTY on an impersonation token. It used to
	// carry the TARGET's platform role, so a `support` admin impersonating a
	// `super_admin` inherited super-admin — a privilege escalation. Impersonation
	// grants the target's ORG context (orgID + orgRole) for support/debugging,
	// never platform-admin powers; the actor uses their own token for those.
	identity := &auth.Identity{
		UserID:         actorUUID,
		OrgID:          orgUUID,
		OrgRole:        orgRole,
		PlatformRole:   "",
		SessionID:      NewID(),
		ActingAsUserID: targetUUID,
	}
	pair, err := s.minter.Mint(ctx, identity)
	if err != nil {
		return nil, w.Wrapf(err, "mint impersonation token")
	}

	// Impersonation changes no row, so there is no mutation for the record to be
	// atomic with — but the token must not reach the caller unless the record is
	// committed, so the write is what gates the response.
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, actorID, "user", EventPlatformImpersonated, "user", req.UserId, "")
	}); err != nil {
		return nil, w.Wrapf(err, "cannot record impersonation")
	}

	return &gen.ImpersonateUserResponse{
		AccessToken: pair.AccessToken,
		ExpiresIn:   int64(AccessTokenLifetime.Seconds()),
	}, nil
}

// ListActiveSessions lists active sessions for a user (support+ only).
func (s *Service) ListActiveSessions(ctx context.Context, actorID string, req *gen.ListActiveSessionsRequest) (*gen.ListActiveSessionsResponse, error) {
	w := wool.Get(ctx).In("ListActiveSessions")

	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	var sessions []*Session
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		sessions, err = s.store.ListActiveSessions(ctx, req.UserId, req.PageSize)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list sessions")
	}

	var infos []*gen.SessionInfo
	for _, sess := range sessions {
		infos = append(infos, &gen.SessionInfo{
			// family_id is the stable per-device session identifier. Row ids
			// rotate with refresh tokens and must not leak into management UX.
			Id:            sess.FamilyID,
			UserId:        sess.UserID,
			IpAddress:     sess.IPAddress,
			DeviceInfo:    sess.DeviceInfo,
			CreatedAt:     timestamppb.New(sess.CreatedAt),
			LastActiveAt:  timestamppb.New(sess.LastActiveAt),
			IdleExpiresAt: timestamppb.New(sess.IdleExpiresAt),
			ExpiresAt:     timestamppb.New(sess.ExpiresAt),
		})
	}

	return &gen.ListActiveSessionsResponse{Sessions: infos}, nil
}

// RevokeSession force-logs-out one active device family by id (support+ only). A platform
// admin acts across all users, so the write rides WithControlPlane (RLS would otherwise scope
// the sessions table to the caller).
func (s *Service) RevokeSession(ctx context.Context, actorID string, req *gen.RevokeSessionRequest) error {
	w := wool.Get(ctx).In("RevokeSession")

	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	reason := req.Reason
	if reason == "" {
		reason = "revoked_by_admin"
	}
	var revokedSessionIDs []string
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		revokedSessionIDs, err = s.store.RevokeSession(ctx, req.SessionId, reason)
		if err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventSessionRevoked, "session", req.SessionId, "")
	}); err != nil {
		return w.Wrapf(err, "cannot revoke session")
	}

	// Killing the refresh family leaves the victim's outstanding access token
	// valid until its natural TTL on every path. Write a session-revocation
	// marker per revoked row so the access half dies now. Best-effort: the DB
	// revocation above is the durable authority, and a store outage bounds the
	// residual exposure to AccessTokenTTL rather than failing the kill.
	if s.minter != nil {
		for _, sessionID := range revokedSessionIDs {
			if err := s.minter.RevokeSessionAccess(ctx, sessionID); err != nil {
				w.Warn("RevokeSessionAccess failed (best-effort)", wool.ErrField(err))
			}
		}
	}

	return nil
}

// GrantPlatformRole grants a platform role to a user (super_admin only).
func (s *Service) GrantPlatformRole(ctx context.Context, actorID string, req *gen.GrantPlatformRoleRequest) error {
	w := wool.Get(ctx).In("GrantPlatformRole")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := s.store.GrantPlatformRole(ctx, req.UserId, req.PlatformRole, actorID); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventPlatformRoleGranted, "user", req.UserId, "")
	}); err != nil {
		return w.Wrapf(err, "cannot grant platform role")
	}

	// Notify the user about their new platform role
	_ = s.NotifyUser(
		ctx,
		req.UserId,
		NotificationCategorySecurity,
		"Platform role granted",
		fmt.Sprintf("You've been granted %s role", req.PlatformRole),
	)
	s.notifySlack(ctx, fmt.Sprintf("Platform role granted: user %s → %s (by %s)", req.UserId, req.PlatformRole, actorID))

	return nil
}

// RevokePlatformRole removes a user's platform admin status (super_admin only).
func (s *Service) RevokePlatformRole(ctx context.Context, actorID string, req *gen.RevokePlatformRoleRequest) error {
	w := wool.Get(ctx).In("RevokePlatformRole")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := s.store.RevokePlatformRole(ctx, req.UserId); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventPlatformRoleRevoked, "user", req.UserId, "")
	}); err != nil {
		return w.Wrapf(err, "cannot revoke platform role")
	}
	return nil
}

// ListPlatformAdmins returns all platform admins (super_admin only).
func (s *Service) ListPlatformAdmins(ctx context.Context, actorID string) (*gen.ListPlatformAdminsResponse, error) {
	w := wool.Get(ctx).In("ListPlatformAdmins")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	admins, err := s.store.ListPlatformAdmins(ctx)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list platform admins")
	}

	var entries []*gen.PlatformAdminEntry
	for _, a := range admins {
		entries = append(entries, &gen.PlatformAdminEntry{
			UserId:       a.UserID,
			PlatformRole: a.PlatformRole,
			GrantedBy:    a.GrantedBy,
		})
	}

	return &gen.ListPlatformAdminsResponse{Admins: entries}, nil
}

// ListFeatureFlags returns the legacy migration inventory (super_admin only).
func (s *Service) ListFeatureFlags(ctx context.Context, actorID string) (*gen.ListFeatureFlagsResponse, error) {
	w := wool.Get(ctx).In("ListFeatureFlags")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	flags, err := s.store.ListFeatureFlags(ctx)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list feature flags")
	}

	var entries []*gen.FeatureFlagEntry
	for _, f := range flags {
		entries = append(entries, &gen.FeatureFlagEntry{
			Name:           f.Name,
			Description:    f.Description,
			Enabled:        f.Enabled,
			RolloutPercent: int32(f.RolloutPercent),
			TargetOrgIds:   f.TargetOrgIDs,
		})
	}

	return &gen.ListFeatureFlagsResponse{Flags: entries}, nil
}
