package business

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/wool"

	"api/pkg/gen"
)

func orgRoleToString(role gen.OrgRole) string {
	switch role {
	case gen.OrgRole_ORG_ROLE_OWNER:
		return "owner"
	case gen.OrgRole_ORG_ROLE_ADMIN:
		return "admin"
	default:
		return "member"
	}
}

// GetOrganization returns an organization by ID.
func (s *Service) GetOrganization(ctx context.Context, req *gen.GetOrganizationRequest) (*gen.Organization, error) {
	w := wool.Get(ctx).In("GetOrganization")

	org, err := s.store.GetOrganization(ctx, req.Id)
	if err != nil {
		return nil, w.Wrapf(err, "cannot get organization")
	}
	return org, nil
}

// ListOrganizations returns all organizations the user belongs to.
func (s *Service) ListOrganizations(ctx context.Context, userID string) (*gen.ListOrganizationsResponse, error) {
	w := wool.Get(ctx).In("ListOrganizations")

	orgs, err := s.store.ListOrganizationsForUser(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list organizations")
	}
	return &gen.ListOrganizationsResponse{Organizations: orgs}, nil
}

// AddOrgMember adds a member to an organization.
func (s *Service) AddOrgMember(ctx context.Context, actorID string, req *gen.AddOrgMemberRequest) error {
	w := wool.Get(ctx).In("AddOrgMember")

	role := orgRoleToString(req.Role)
	if err := s.store.AddOrgMember(ctx, req.OrgId, req.UserId, role); err != nil {
		return w.Wrapf(err, "cannot add member")
	}

	// Tell the cache the old "not a member" entry is stale — without this,
	// the first request from the newly-added user would spend 30s hitting
	// the cache with the wrong negative answer. No-op when caching is off.
	s.invalidateMembership(ctx, req.OrgId, req.UserId)

	s.emit(ctx, actorID, "user", "org.member_added", "organization", req.OrgId, req.OrgId)

	// Notify the added user
	org, _ := s.store.GetOrganization(ctx, req.OrgId)
	orgName := req.OrgId
	if org != nil {
		orgName = org.Name
	}
	_ = s.NotifyUser(ctx, req.UserId, "Organization membership", fmt.Sprintf("You were added to %s", orgName))

	return nil
}

// RemoveOrgMember removes a member from an organization.
//
// Guards:
//   - Last-owner guard: if the target is the only remaining owner/admin,
//     reject. Otherwise we'd leave the org with no one who can manage it.
//   - Cascade: also remove the target's team memberships within the org,
//     otherwise a removed user still holds team access via orphaned rows.
func (s *Service) RemoveOrgMember(ctx context.Context, actorID string, req *gen.RemoveOrgMemberRequest) error {
	w := wool.Get(ctx).In("RemoveOrgMember")

	// Fetch current members once; use for both the last-admin check and
	// (implicitly) the emit audit.
	members, err := s.store.ListOrgMembers(ctx, req.OrgId)
	if err != nil {
		return w.Wrapf(err, "cannot load org members for guard")
	}
	var adminCount int
	targetIsAdmin := false
	for _, m := range members {
		if m.Role != gen.OrgRole_ORG_ROLE_ADMIN && m.Role != gen.OrgRole_ORG_ROLE_OWNER {
			continue
		}
		adminCount++
		if m.UserId == req.UserId {
			targetIsAdmin = true
		}
	}
	if targetIsAdmin && adminCount <= 1 {
		return w.NewError("cannot remove the last admin/owner from the organization")
	}

	if err := s.store.RemoveOrgMember(ctx, req.OrgId, req.UserId); err != nil {
		return w.Wrapf(err, "cannot remove member")
	}

	// Invalidate the membership cache — otherwise the removed user
	// keeps passing authorization checks for up to 30s while their
	// cached entry is still "admin" or "member".
	s.invalidateMembership(ctx, req.OrgId, req.UserId)

	// Cascade: unwind team memberships in this org for the removed user.
	// Store may or may not expose a bulk delete; iterate teams best-effort.
	teams, tErr := s.store.ListTeams(ctx, req.OrgId)
	if tErr == nil {
		for _, t := range teams {
			_ = s.store.RemoveTeamMember(ctx, t.Id, req.UserId)
		}
	}

	s.emit(ctx, actorID, "user", "org.member_removed", "organization", req.OrgId, req.OrgId)
	return nil
}

// ListOrgMembers lists all members of an organization.
func (s *Service) ListOrgMembers(ctx context.Context, req *gen.ListOrgMembersRequest) (*gen.ListOrgMembersResponse, error) {
	w := wool.Get(ctx).In("ListOrgMembers")

	members, err := s.store.ListOrgMembers(ctx, req.OrgId)
	if err != nil {
		return nil, w.Wrapf(err, "cannot list members")
	}
	return &gen.ListOrgMembersResponse{Members: members}, nil
}
