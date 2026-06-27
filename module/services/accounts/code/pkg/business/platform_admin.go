package business

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/auth"
	"accounts/pkg/gen"
)

// Platform role hierarchy for authorization checks (defense-in-depth, OPA is primary).
var platformRoleLevel = map[string]int{
	"super_admin": 3,
	"billing":     2,
	"support":     1,
}

// requirePlatformRole checks that the actor has at least the given platform role.
func (s *Service) requirePlatformRole(ctx context.Context, actorID, minRole string) error {
	role, err := s.store.GetPlatformRole(ctx, actorID)
	if err != nil {
		return err
	}
	if role == "" {
		return fmt.Errorf("not a platform admin")
	}
	if platformRoleLevel[role] < platformRoleLevel[minRole] {
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

	users, nextToken, err := s.store.SearchUsers(ctx, req.Query, req.PageSize, req.PageToken)
	if err != nil {
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

	if err := s.store.UpdateUserStatus(ctx, req.UserId, "suspended"); err != nil {
		return w.Wrapf(err, "cannot suspend user")
	}

	// Revoke all sessions for the suspended user
	_ = s.store.RevokeAllUserSessions(ctx, req.UserId, "user_suspended")

	s.emit(ctx, actorID, "user", "user.suspended", "user", req.UserId, "")
	s.notifySlack(ctx, fmt.Sprintf("Security: user %s suspended by %s (reason: %s)", req.UserId, actorID, req.Reason))
	return nil
}

// UnsuspendUser reactivates a suspended user account (super_admin only).
func (s *Service) UnsuspendUser(ctx context.Context, actorID string, req *gen.UnsuspendUserRequest) error {
	w := wool.Get(ctx).In("UnsuspendUser")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.UpdateUserStatus(ctx, req.UserId, "active"); err != nil {
		return w.Wrapf(err, "cannot unsuspend user")
	}

	s.emit(ctx, actorID, "user", "user.unsuspended", "user", req.UserId, "")
	return nil
}

// ImpersonateUser issues a token as another user (support+ only).
// UserID on the minted Identity stays as the actor (for audit), ActingAsUserID
// points at the target — downstream business logic authorises against the
// target, audit logs against the actor.
func (s *Service) ImpersonateUser(ctx context.Context, actorID string, req *gen.ImpersonateUserRequest) (*gen.ImpersonateUserResponse, error) {
	w := wool.Get(ctx).In("ImpersonateUser")

	if s.minter == nil {
		return nil, w.NewError("auth path not wired: minter missing")
	}
	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	// Cross-tenant lookup: a platform admin impersonating any user
	// needs to see all their orgs + role regardless of caller's
	// tenant. WithBypass elevates for the read; the impersonation
	// session that gets minted carries the resolved orgID so
	// downstream tenant-scoped ops run correctly under the target's
	// org.
	var orgs []*gen.Organization
	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		os, err := s.store.ListOrganizationsForUser(ctx, req.UserId)
		orgs = os
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list orgs for target user")
	}

	orgID := ""
	orgRole := ""
	if len(orgs) > 0 {
		orgID = orgs[0].Id
		var members []*gen.OrgMembership
		if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
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

	// The minted Identity names the actor as UserID (audit correlation) and
	// the target via ActingAsUserID. Downstream services read ActingAsUserID
	// for authz decisions when present.
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

	s.emit(ctx, actorID, "user", "platform.user_impersonated", "user", req.UserId, "")

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

	sessions, err := s.store.ListActiveSessions(ctx, req.UserId, req.PageSize)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list sessions")
	}

	var infos []*gen.SessionInfo
	for _, sess := range sessions {
		infos = append(infos, &gen.SessionInfo{
			Id:           sess.ID,
			UserId:       sess.UserID,
			IpAddress:    sess.IPAddress,
			CreatedAt:    timestamppb.New(sess.CreatedAt),
			LastActiveAt: timestamppb.New(sess.LastActiveAt),
		})
	}

	return &gen.ListActiveSessionsResponse{Sessions: infos}, nil
}

// GrantPlatformRole grants a platform role to a user (super_admin only).
func (s *Service) GrantPlatformRole(ctx context.Context, actorID string, req *gen.GrantPlatformRoleRequest) error {
	w := wool.Get(ctx).In("GrantPlatformRole")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.GrantPlatformRole(ctx, req.UserId, req.PlatformRole, actorID); err != nil {
		return w.Wrapf(err, "cannot grant platform role")
	}

	s.emit(ctx, actorID, "user", "platform.role_granted", "user", req.UserId, "")

	// Notify the user about their new platform role
	_ = s.NotifyUser(ctx, req.UserId, "Platform role granted", fmt.Sprintf("You've been granted %s role", req.PlatformRole))
	s.notifySlack(ctx, fmt.Sprintf("Platform role granted: user %s → %s (by %s)", req.UserId, req.PlatformRole, actorID))

	return nil
}

// RevokePlatformRole removes a user's platform admin status (super_admin only).
func (s *Service) RevokePlatformRole(ctx context.Context, actorID string, req *gen.RevokePlatformRoleRequest) error {
	w := wool.Get(ctx).In("RevokePlatformRole")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return w.Wrapf(err, "permission denied")
	}

	if err := s.store.RevokePlatformRole(ctx, req.UserId); err != nil {
		return w.Wrapf(err, "cannot revoke platform role")
	}

	s.emit(ctx, actorID, "user", "platform.role_revoked", "user", req.UserId, "")
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

// ListFeatureFlags returns all feature flags (super_admin only).
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

// UpsertFeatureFlag creates or updates a feature flag (super_admin only).
func (s *Service) UpsertFeatureFlag(ctx context.Context, actorID string, req *gen.UpsertFeatureFlagRequest) (*gen.UpsertFeatureFlagResponse, error) {
	w := wool.Get(ctx).In("UpsertFeatureFlag")

	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, w.Wrapf(err, "permission denied")
	}

	flag := &FeatureFlag{
		Name:           req.Name,
		Description:    req.Description,
		Enabled:        req.Enabled,
		RolloutPercent: int(req.RolloutPercent),
		TargetOrgIDs:   req.TargetOrgIds,
	}
	if err := s.store.UpsertFeatureFlag(ctx, flag); err != nil {
		return nil, w.Wrapf(err, "cannot upsert feature flag")
	}

	s.emit(ctx, actorID, "user", "feature_flag.updated", "feature_flag", req.Name, "")

	return &gen.UpsertFeatureFlagResponse{Name: req.Name}, nil
}
