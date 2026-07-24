package business

import (
	"context"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func (s *Service) getUserAsSelf(ctx context.Context, userID string) (*gen.User, error) {
	var user *gen.User
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var err error
		user, err = s.store.GetUser(ctx, userID)
		return err
	}); err != nil {
		return nil, err
	}
	return user, nil
}

// GetSelf returns the full profile for the authenticated user.
func (s *Service) GetSelf(ctx context.Context, userID string) (*gen.GetSelfResponse, error) {
	w := wool.Get(ctx).In("GetSelf")

	// Profile and linked identities are both user-scoped. Keep them in one
	// transaction so the caller identity is established once and both reads
	// fail closed if a future query accidentally targets another user.
	var user *gen.User
	var identities []*gen.UserIdentity
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		u, err := s.store.GetUser(ctx, userID)
		if err != nil {
			return err
		}
		ids, err := s.store.ListUserIdentities(ctx, userID)
		if err != nil {
			return err
		}
		user = u
		identities = ids
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "cannot load self profile")
	}

	// /me returns every org the caller is a member of for the org
	// switcher — inherently cross-tenant. organizations is RLS-
	// protected (Phase 2F) so the read needs WithControlPlane; the SQL
	// already filters by user_id, so no leakage.
	var orgs []*gen.Organization
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
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

// GetUser returns a user by UUID or email under an explicit data-access
// identity. A human identity can see only itself; System is reserved for a
// platform-admin handler that completed authorization before this call.
func (s *Service) GetUser(ctx context.Context, access Identity, req *gen.GetUserRequest) (*gen.User, error) {
	w := wool.Get(ctx).In("GetUser")

	var user *gen.User
	if err := s.store.As(access).Within(ctx, func(ctx context.Context) error {
		var err error
		switch {
		case req.GetUuid() != "":
			user, err = s.store.GetUser(ctx, req.GetUuid())
		case req.GetEmail() != "":
			user, err = s.store.GetUserByEmail(ctx, req.GetEmail())
		default:
			return w.NewError("uuid or email required")
		}
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get user")
	}
	return user, nil
}

// ListUsers returns paginated users within an org.
func (s *Service) ListUsers(ctx context.Context, req *gen.ListUsersRequest) (*gen.ListUsersResponse, error) {
	w := wool.Get(ctx).In("ListUsers")

	statusFilter := ""
	if req.Status != gen.UserStatus_USER_STATUS_UNSPECIFIED {
		statusFilter = req.Status.String()
	}

	var users []*gen.User
	var nextToken string
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var err error
		users, nextToken, err = s.store.ListUsers(ctx, "", statusFilter, req.PageSize, req.PageToken)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list users")
	}

	return &gen.ListUsersResponse{
		Users:         users,
		NextPageToken: nextToken,
	}, nil
}

// UpdateUser updates a user's profile fields.
func (s *Service) UpdateUser(ctx context.Context, actorID string, access Identity, userID string, req *gen.UpdateUserRequest) (*gen.User, error) {
	w := wool.Get(ctx).In("UpdateUser")

	updates := map[string]any{}
	if req.User != nil {
		if req.User.PrimaryEmail != "" {
			updates["primary_email"] = req.User.PrimaryEmail
		}
		if req.User.Profile != nil {
			// Merge, not replace: the caller sends only the fields it changed,
			// so a self-service profile write can't clobber a concurrent one.
			// GDPR anonymization keeps the replace path (store UpdateUser's
			// "profile" key) to guarantee a full scrub.
			updates["profile_merge"] = req.User.Profile
		}
	}

	var user *gen.User
	if err := s.store.As(access).Within(ctx, func(ctx context.Context) error {
		var err error
		if len(updates) == 0 {
			user, err = s.store.GetUser(ctx, userID)
			return err
		}
		user, err = s.store.UpdateUser(ctx, userID, updates)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot update user")
	}

	s.emit(ctx, actorID, "user", "user.updated", "user", userID, "")
	return user, nil
}

// DeleteUser soft-deletes a user.
func (s *Service) DeleteUser(ctx context.Context, actorID string, access Identity, req *gen.GetUserRequest) error {
	w := wool.Get(ctx).In("DeleteUser")

	targetID := req.GetUuid()
	if targetID == "" {
		return w.NewError("uuid required for delete")
	}
	if err := s.store.As(access).Within(ctx, func(ctx context.Context) error {
		return s.store.DeleteUser(ctx, targetID)
	}); err != nil {
		return w.Wrapf(err, "cannot delete user")
	}

	s.emit(ctx, actorID, "user", "user.deleted", "user", targetID, "")
	return nil
}

// AddIdentity adds a new identity to a user.
// Audited with action "user.identity_added" — adding identities is a
// security-relevant event (the user can now log in via a new provider).
func (s *Service) AddIdentity(ctx context.Context, actorID string, access Identity, req *gen.AddIdentityRequest) (*gen.UserIdentity, error) {
	w := wool.Get(ctx).In("AddIdentity")

	identity := &gen.UserIdentity{
		Uuid:          NewIDString(),
		UserUuid:      req.UserUuid,
		Provider:      req.Identity.Provider,
		ProviderId:    req.Identity.ProviderId,
		ProviderEmail: req.Identity.ProviderEmail,
		EmailVerified: req.Identity.EmailVerified,
	}

	if err := s.store.As(access).Within(ctx, func(ctx context.Context) error {
		return s.store.AddIdentity(ctx, identity)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot add identity")
	}

	s.emit(ctx, actorID, "user", "user.identity_added", "identity", identity.Uuid, "")
	return identity, nil
}

// FindUserByIdentity finds a user by provider identity.
func (s *Service) FindUserByIdentity(ctx context.Context, req *gen.FindUserByIdentityRequest) (*gen.User, error) {
	w := wool.Get(ctx).In("FindUserByIdentity")

	var user *gen.User
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var err error
		user, err = s.store.FindUserByIdentity(ctx, req.Provider, req.ProviderId)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot find user")
	}
	return user, nil
}

// ListUserIdentities returns all identities for a user.
func (s *Service) ListUserIdentities(ctx context.Context, access Identity, req *gen.ListUserIdentitiesRequest) (*gen.ListUserIdentitiesResponse, error) {
	w := wool.Get(ctx).In("ListUserIdentities")

	var identities []*gen.UserIdentity
	if err := s.store.As(access).Within(ctx, func(ctx context.Context) error {
		var err error
		identities, err = s.store.ListUserIdentities(ctx, req.UserUuid)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list identities")
	}
	return &gen.ListUserIdentitiesResponse{Identities: identities}, nil
}
