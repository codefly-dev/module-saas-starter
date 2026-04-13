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
func (s *Service) RemoveOrgMember(ctx context.Context, actorID string, req *gen.RemoveOrgMemberRequest) error {
	w := wool.Get(ctx).In("RemoveOrgMember")

	if err := s.store.RemoveOrgMember(ctx, req.OrgId, req.UserId); err != nil {
		return w.Wrapf(err, "cannot remove member")
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
