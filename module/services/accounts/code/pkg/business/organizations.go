package business

import (
	"context"
	"fmt"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
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

// GetOrganization returns an organization by ID. organizations is
// RLS-protected with a self-referential policy (id matches current
// setting), so the read goes through WithOrgTx scoped to req.Id.
// Handler authz upstream (org membership) gates who's allowed to ask.
func (s *Service) GetOrganization(ctx context.Context, req *gen.GetOrganizationRequest) (*gen.Organization, error) {
	w := wool.Get(ctx).In("GetOrganization")

	var org *gen.Organization
	if err := s.store.WithOrgTx(ctx, req.Id, func(ctx context.Context) error {
		o, err := s.store.GetOrganization(ctx, req.Id)
		org = o
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get organization")
	}
	return org, nil
}

// ListOrganizations returns all organizations the user belongs to.
//
// Cross-tenant by nature: a user can be a member of multiple orgs,
// and the org switcher needs to see every one of them. WithControlPlane
// is the right wrapper — handler authz already proved the caller is
// who they say they are; the SQL filters by user_id so no cross-user
// leakage either.
func (s *Service) ListOrganizations(ctx context.Context, userID string) (*gen.ListOrganizationsResponse, error) {
	w := wool.Get(ctx).In("ListOrganizations")

	var orgs []*gen.Organization
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		os, err := s.store.ListOrganizationsForUser(ctx, userID)
		orgs = os
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list organizations")
	}
	return &gen.ListOrganizationsResponse{Organizations: orgs}, nil
}

// AddOrgMember adds a member to an organization. The seat decision and
// membership write share one tenant transaction and one per-org quota lock, so
// concurrent admin requests cannot consume the same final seat. Updating an
// existing member remains idempotent even when the organization is full.
func (s *Service) AddOrgMember(ctx context.Context, actorID string, req *gen.AddOrgMemberRequest) error {
	w := wool.Get(ctx).In("AddOrgMember")

	role := orgRoleToString(req.Role)
	var orgName string
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		quota, err := s.cardinalityQuotaInTx(ctx, req.OrgId, EntitlementSeats)
		if err != nil {
			return w.Wrapf(err, "cannot check seat quota")
		}
		exists, err := s.store.OrgMemberExists(ctx, req.OrgId, req.UserId)
		if err != nil {
			return w.Wrapf(err, "cannot check organization membership")
		}
		if !exists {
			if err := quota.RequireAvailable(); err != nil {
				return err
			}
		}
		if err := s.store.AddOrgMember(ctx, req.OrgId, req.UserId, role); err != nil {
			return err
		}
		// Read org name within the same tx so RLS lets us through.
		if o, err := s.store.GetOrganization(ctx, req.OrgId); err == nil && o != nil {
			orgName = o.Name
		}
		return s.emitTx(ctx, actorID, "user", EventOrgMemberAdded, "organization", req.OrgId, req.OrgId)
	}); err != nil {
		return w.Wrapf(err, "cannot add member")
	}

	// Tell the cache the old "not a member" entry is stale — without this,
	// the first request from the newly-added user would spend 30s hitting
	// the cache with the wrong negative answer. No-op when caching is off.
	_ = s.invalidateMembership(ctx, req.OrgId, req.UserId)

	if orgName == "" {
		orgName = req.OrgId
	}
	_ = s.NotifyUser(
		ctx,
		req.UserId,
		NotificationCategoryProduct,
		"Organization membership",
		fmt.Sprintf("You were added to %s", orgName),
	)

	return nil
}

// ConvergeFixtureOrgMember applies fixture-declared membership as bootstrap
// state. It bypasses runtime seat admission and user-facing side effects while
// preserving the cache consistency required by every membership mutation.
func (s *Service) ConvergeFixtureOrgMember(ctx context.Context, req *gen.AddOrgMemberRequest) error {
	w := wool.Get(ctx).In("ConvergeFixtureOrgMember")
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		return s.store.AddOrgMember(ctx, req.OrgId, req.UserId, orgRoleToString(req.Role))
	}); err != nil {
		return w.Wrapf(err, "cannot converge fixture member")
	}
	_ = s.invalidateMembership(ctx, req.OrgId, req.UserId)
	return nil
}

// RemoveOrgMember removes a member from an organization together with the team
// authority that depends on that membership.
//
// Guards:
//   - Last-owner guard: if the target is the only remaining owner/admin,
//     reject. Otherwise we'd leave the org with no one who can manage it.
//   - Dependent access: team_members is the one relation that still confers
//     live permissions after the organization membership is gone — permission
//     resolution matches team-subject role assignments through it — so those
//     rows are deleted in the same transaction. Organization-scoped role
//     assignments and installation ownership are deliberately left in place:
//     both are gated on current organization membership at read time, so they
//     are already ineffective for a departed member.
func (s *Service) RemoveOrgMember(ctx context.Context, actorID string, req *gen.RemoveOrgMemberRequest) error {
	w := wool.Get(ctx).In("RemoveOrgMember")

	// Lock, last-admin guard, dependent-access delete, membership delete, and
	// the audit event all run inside one org-scoped WithOrgTx: org_members RLS
	// + organizations RLS both let the queries through, and the record cannot
	// commit describing a removal whose dependent access is still standing.
	//
	// Lock first, before any read the decision depends on: a concurrent team
	// insert for the same (org, user) either commits before the guard reads or
	// waits behind this transaction, so it can neither be missed by the delete
	// nor land after it.
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		if err := s.store.LockOrgMembership(ctx, req.OrgId, req.UserId); err != nil {
			return w.Wrapf(err, "cannot lock org membership")
		}
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
		// Dependent access before the parent row: migration 127 made team_members
		// a child of organization_members with ON DELETE CASCADE, so deleting the
		// membership first would leave this statement nothing to find and its
		// reported count permanently zero. Removing explicitly keeps that count
		// truthful; the cascade stays as the backstop for any writer that does
		// not come through here.
		if _, err := s.store.RemoveOrgTeamMemberships(ctx, req.OrgId, req.UserId); err != nil {
			return w.Wrapf(err, "cannot remove dependent team memberships")
		}
		if err := s.store.RemoveOrgMember(ctx, req.OrgId, req.UserId); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventOrgMemberRemoved, "organization", req.OrgId, req.OrgId)
	}); err != nil {
		return w.Wrapf(err, "cannot remove member")
	}

	// Invalidate the membership cache — otherwise the removed user
	// keeps passing authorization checks for up to 30s while their
	// cached entry is still "admin" or "member". The removal is committed by
	// now, so a failure here is reported, not raised: turning it into an error
	// would tell the caller the removal did not happen when it did.
	_ = s.invalidateMembership(ctx, req.OrgId, req.UserId)

	return nil
}

// ListOrgMembers lists all members of an organization.
func (s *Service) ListOrgMembers(ctx context.Context, req *gen.ListOrgMembersRequest) (*gen.ListOrgMembersResponse, error) {
	w := wool.Get(ctx).In("ListOrgMembers")

	var members []*gen.OrgMembership
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		ms, err := s.store.ListOrgMembers(ctx, req.OrgId)
		members = ms
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list members")
	}
	return &gen.ListOrgMembersResponse{Members: members}, nil
}
