package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/gen"
)

// GetSelf returns the full profile for the authenticated user.
func (s *Service) GetSelf(ctx context.Context, userID string) (*gen.GetSelfResponse, error) {
	w := wool.Get(ctx).In("GetSelf")

	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot get user")
	}

	identities, err := s.store.ListUserIdentities(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list identities")
	}

	// /me returns every org the caller is a member of for the org
	// switcher — inherently cross-tenant. organizations is RLS-
	// protected (Phase 2F) so the read needs WithBypass; the SQL
	// already filters by user_id, so no leakage.
	var orgs []*gen.Organization
	if err := s.store.WithBypass(ctx, func(ctx context.Context) error {
		os, err := s.store.ListOrganizationsForUser(ctx, userID)
		orgs = os
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list organizations")
	}

	return &gen.GetSelfResponse{
		User:          user,
		Identities:    identities,
		Organizations: orgs,
	}, nil
}

// GetUser returns a user by UUID or email.
func (s *Service) GetUser(ctx context.Context, req *gen.GetUserRequest) (*gen.User, error) {
	w := wool.Get(ctx).In("GetUser")

	if uuid := req.GetUuid(); uuid != "" {
		user, err := s.store.GetUser(ctx, uuid)
		if err != nil {
			return nil, w.Wrapf(err, "cannot get user")
		}
		return user, nil
	}

	if email := req.GetEmail(); email != "" {
		user, err := s.store.GetUserByEmail(ctx, email)
		if err != nil {
			return nil, w.Wrapf(err, "cannot get user by email")
		}
		return user, nil
	}

	return nil, w.NewError("uuid or email required")
}

// ListUsers returns paginated users within an org.
func (s *Service) ListUsers(ctx context.Context, req *gen.ListUsersRequest) (*gen.ListUsersResponse, error) {
	w := wool.Get(ctx).In("ListUsers")

	statusFilter := ""
	if req.Status != gen.UserStatus_USER_STATUS_UNSPECIFIED {
		statusFilter = req.Status.String()
	}

	users, nextToken, err := s.store.ListUsers(ctx, "", statusFilter, req.PageSize, req.PageToken)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list users")
	}

	return &gen.ListUsersResponse{
		Users:         users,
		NextPageToken: nextToken,
	}, nil
}

// UpdateUser updates a user's profile fields.
func (s *Service) UpdateUser(ctx context.Context, userID string, req *gen.UpdateUserRequest) (*gen.User, error) {
	w := wool.Get(ctx).In("UpdateUser")

	updates := map[string]any{}
	if req.User != nil {
		if req.User.PrimaryEmail != "" {
			updates["primary_email"] = req.User.PrimaryEmail
		}
		if req.User.Profile != nil {
			updates["profile"] = req.User.Profile
		}
	}

	if len(updates) == 0 {
		return s.store.GetUser(ctx, userID)
	}

	// The users row is RLS-protected (users_update: uuid == app.current_user_id).
	// Scope to the TARGET user so the GUC is set — this permits both the self-edit
	// and the admin-edits-another case (the row being updated IS the scoped id).
	// Without it the UPDATE silently matches zero rows under the app_tenant role.
	var user *gen.User
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		u, e := s.store.UpdateUser(ctx, userID, updates)
		user = u
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot update user")
	}

	s.emit(ctx, userID, "user", "user.updated", "user", userID, "")
	return user, nil
}

// DeleteUser soft-deletes a user.
func (s *Service) DeleteUser(ctx context.Context, userID string, req *gen.GetUserRequest) error {
	w := wool.Get(ctx).In("DeleteUser")

	targetID := req.GetUuid()
	if targetID == "" {
		return w.NewError("uuid required for delete")
	}
	// Scope to the target so the RLS users_delete policy (uuid == app.current_user_id)
	// permits the soft-delete; the session revoke rides the same scope.
	if err := s.store.As(Identity{UserID: targetID}).Within(ctx, func(ctx context.Context) error {
		if err := s.store.DeleteUser(ctx, targetID); err != nil {
			return err
		}
		// Revoke all sessions (best-effort)
		_ = s.store.RevokeAllUserSessions(ctx, targetID, "user_deleted")
		return nil
	}); err != nil {
		return w.Wrapf(err, "cannot delete user")
	}

	s.emit(ctx, userID, "user", "user.deleted", "user", targetID, "")
	return nil
}

// AddIdentity adds a new identity to a user.
// Audited with action "user.identity_added" — adding identities is a
// security-relevant event (the user can now log in via a new provider).
func (s *Service) AddIdentity(ctx context.Context, req *gen.AddIdentityRequest) (*gen.UserIdentity, error) {
	w := wool.Get(ctx).In("AddIdentity")

	identity := &gen.UserIdentity{
		Uuid:          NewIDString(),
		UserUuid:      req.UserUuid,
		Provider:      req.Identity.Provider,
		ProviderId:    req.Identity.ProviderId,
		ProviderEmail: req.Identity.ProviderEmail,
		EmailVerified: req.Identity.EmailVerified,
	}

	if err := s.store.AddIdentity(ctx, identity); err != nil {
		return nil, w.Wrapf(err, "cannot add identity")
	}

	s.emit(ctx, req.UserUuid, "user", "user.identity_added", "identity", identity.Uuid, "")
	return identity, nil
}

// FindUserByIdentity finds a user by provider identity.
func (s *Service) FindUserByIdentity(ctx context.Context, req *gen.FindUserByIdentityRequest) (*gen.User, error) {
	w := wool.Get(ctx).In("FindUserByIdentity")

	user, err := s.store.FindUserByIdentity(ctx, req.Provider, req.ProviderId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot find user")
	}
	return user, nil
}

// ListUserIdentities returns all identities for a user.
func (s *Service) ListUserIdentities(ctx context.Context, req *gen.ListUserIdentitiesRequest) (*gen.ListUserIdentitiesResponse, error) {
	w := wool.Get(ctx).In("ListUserIdentities")

	identities, err := s.store.ListUserIdentities(ctx, req.UserUuid)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list identities")
	}
	return &gen.ListUserIdentitiesResponse{Identities: identities}, nil
}
