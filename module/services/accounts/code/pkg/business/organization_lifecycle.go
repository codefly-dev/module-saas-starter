package business

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// OrganizationCreationPolicy says who may create an organization on this
// deployment. It never applies to the personal organization registration
// creates, to fixture seeding, or to a platform super administrator.
type OrganizationCreationPolicy string

const (
	// OrganizationCreationOpen lets every signed-in user create one.
	OrganizationCreationOpen OrganizationCreationPolicy = "open"
	// OrganizationCreationPlatformAdmin reserves creation to platform
	// administrators; everyone else joins by invitation.
	OrganizationCreationPlatformAdmin OrganizationCreationPolicy = "platform_admin"
	// OrganizationCreationDisabled refuses creation through the API entirely.
	OrganizationCreationDisabled OrganizationCreationPolicy = "disabled"
)

// ParseOrganizationCreationPolicy reads the ORGANIZATION_CREATION value. Empty
// keeps the historical behavior, open; anything unrecognized is an error, so a
// typo cannot quietly leave creation open.
func ParseOrganizationCreationPolicy(raw string) (OrganizationCreationPolicy, error) {
	switch policy := OrganizationCreationPolicy(strings.ToLower(strings.TrimSpace(raw))); policy {
	case "":
		return OrganizationCreationOpen, nil
	case OrganizationCreationOpen, OrganizationCreationPlatformAdmin, OrganizationCreationDisabled:
		return policy, nil
	default:
		return "", fmt.Errorf("ORGANIZATION_CREATION must be one of: open, platform_admin, disabled (got %q)", raw)
	}
}

// SetOrganizationCreationPolicy installs the deployment's creation policy.
func (s *Service) SetOrganizationCreationPolicy(policy OrganizationCreationPolicy) {
	s.orgCreationPolicy = policy
}

var (
	// ErrOrganizationCreationRefused is the creation policy refusing the caller.
	ErrOrganizationCreationRefused = errors.New("this deployment does not let you create organizations; ask an administrator to invite you")
	// ErrOrganizationOwnerRefused is a caller naming someone else as the owner
	// of a new organization without platform authority to do so.
	ErrOrganizationOwnerRefused = errors.New("only a platform administrator may create an organization for someone else")
	// ErrOrgSoleMember is a member leaving an organization nobody else is in.
	ErrOrgSoleMember = errors.New("you are this organization's only member; delete it instead of leaving")
	// ErrOrgDeleteConfirmation is a delete whose typed slug does not match.
	ErrOrgDeleteConfirmation = errors.New("the confirmation does not match the organization's slug")
	// ErrPublicOriginUnavailable is a link that cannot be minted because no
	// operator-trusted public origin is known.
	ErrPublicOriginUnavailable = errors.New("public application origin is unavailable")
)

// isPlatformRole reports whether actorID holds at least minRole on the platform.
// A failed lookup is an error, never a quiet "no".
func (s *Service) isPlatformRole(ctx context.Context, actorID, minRole string) (bool, error) {
	role, err := s.store.GetPlatformRole(ctx, actorID)
	if err != nil {
		return false, err
	}
	return role != "" && PlatformRoleRank(role) >= PlatformRoleRank(minRole), nil
}

// mayCreateOrganization applies the creation policy to one caller.
func (s *Service) mayCreateOrganization(ctx context.Context, actorID string) (bool, error) {
	superAdmin, err := s.isPlatformRole(ctx, actorID, "super_admin")
	if err != nil {
		return false, err
	}
	if superAdmin {
		return true, nil
	}
	switch s.orgCreationPolicy {
	case OrganizationCreationPlatformAdmin:
		return s.isPlatformRole(ctx, actorID, "support")
	case OrganizationCreationDisabled:
		return false, nil
	default:
		return true, nil
	}
}

// UpdateOrganization renames an organization or changes its slug.
func (s *Service) UpdateOrganization(ctx context.Context, actorID string, req *gen.UpdateOrganizationRequest) (*gen.Organization, error) {
	w := wool.Get(ctx).In("UpdateOrganization")
	var org *gen.Organization
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		var err error
		org, err = s.store.UpdateOrganization(ctx, req.OrgId, strings.TrimSpace(req.Name), req.Slug)
		if err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventOrgUpdated, "organization", req.OrgId, req.OrgId, map[string]any{
			"name": org.Name,
			"slug": org.Slug,
		})
	}); err != nil {
		return nil, w.Wrapf(err, "cannot update organization")
	}
	return org, nil
}

// LeaveOrganization removes the caller's own membership, with everything an
// administrator's removal of them would remove. Two refusals: the caller is
// the organization's only member — there is nobody to leave it to, so the
// answer is to delete it — or the caller is its last administrator while
// others remain, who would be stranded in an organization nobody can manage.
func (s *Service) LeaveOrganization(ctx context.Context, userID string, req *gen.LeaveOrganizationRequest) error {
	w := wool.Get(ctx).In("LeaveOrganization")
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		if err := s.store.LockOrgAdministration(ctx, req.OrgId); err != nil {
			return w.Wrapf(err, "cannot lock organization administration")
		}
		members, err := s.store.CountOrgMembers(ctx, req.OrgId)
		if err != nil {
			return err
		}
		if members <= 1 {
			return ErrOrgSoleMember
		}
		if err := s.removeOrgMembershipTx(ctx, userID, req.OrgId, userID); err != nil {
			return err
		}
		return s.emitTx(ctx, userID, "user", EventOrgMemberLeft, "organization", req.OrgId, req.OrgId)
	}); err != nil {
		return w.Wrapf(err, "cannot leave organization")
	}
	_ = s.invalidateMembership(ctx, req.OrgId, userID)
	return nil
}

// DeleteOrganization archives an organization. In one organization
// transaction it revokes the organization's API keys, pending invitations,
// installations and source delegations, removes every membership (team
// memberships cascade, and the membership trigger revokes each former member's
// sessions bound to it), marks the row archived and releases its slug. With no
// membership left, every request-path authorization refuses the organization,
// and migration 16's trigger keeps it empty. History — audit, approvals, the
// row itself — is kept.
//
// Authorization (owner, or platform super administrator) is the handler's; the
// typed slug confirmation is checked here, against the row under the lock.
//
// A NARROWING, and the widest one an organization has, so it runs under the
// policy log: the entry is appended to the external record and receipted BEFORE
// anything is revoked, and every revocation commits in the same transaction as
// the receipt's commit. A host that cannot witness the append refuses the
// deletion rather than performing one a restore could silently undo.
//
// ONE WITNESSED OPERATION, NOT ONE PER INSTALLATION — the decision this path
// forces, and deletedOrganizationPolicyLogEntry carries the reasoning. The
// short form: everything revoked here is revoked BECAUSE the organization was
// archived, so the archive is the fact worth recording and the organization is
// the subject a replay can ask about; an entry per installation would record
// that same fact several times. The alternative is also not available —
// WithPolicyLoggedNarrowing cannot nest (each call opens its own
// WithControlPlane transaction, and tenant_tx.go's no-nesting rule is that the
// second checks out a second pool connection while the first still holds one),
// so the per-installation entries could not be appended from inside this
// transaction at all. That is why the loop below calls uninstallSolutionTx —
// the same function an operator's uninstall applies inside ITS witnessed
// operation — rather than UninstallSolution, which would try to witness each
// installation separately.
//
// The transaction is the policy log's control-plane one rather than this
// organization's, because the receipt lives in a control-plane relation
// app_tenant holds no grant on, and the receipt and the revocations have to be
// one transaction or "applied but unwitnessed" becomes reachable. The
// organization's own scoping is not lost: every statement below names req.OrgId
// explicitly, which is what the tenant policy would have checked.
func (s *Service) DeleteOrganization(ctx context.Context, actorID string, req *gen.DeleteOrganizationRequest) error {
	w := wool.Get(ctx).In("DeleteOrganization")
	actorType := s.actorTypeForCreator(ctx, actorID)
	var members []*gen.OrgMembership
	if err := s.WithPolicyLoggedNarrowing(ctx,
		deletedOrganizationPolicyLogEntry(actorID, req.OrgId),
		func(ctx context.Context) error {
			if err := s.store.LockOrgAdministration(ctx, req.OrgId); err != nil {
				return w.Wrapf(err, "cannot lock organization administration")
			}
			org, err := s.store.GetOrganization(ctx, req.OrgId)
			if err != nil {
				return err
			}
			if org == nil || org.ArchivedAt != nil {
				return NewStoreError(fmt.Errorf("organization %s not found", req.OrgId), ErrTypeNotFound)
			}
			if !strings.EqualFold(strings.TrimSpace(req.ConfirmSlug), org.Slug) {
				return ErrOrgDeleteConfirmation
			}
			if members, err = s.store.ListOrgMembers(ctx, req.OrgId); err != nil {
				return err
			}
			if _, err := s.store.RevokeOrgAPIKeys(ctx, req.OrgId); err != nil {
				return err
			}
			if _, err := s.store.RevokeOrgPendingInvitations(ctx, req.OrgId); err != nil {
				return err
			}
			installations, err := s.store.ActiveInstallationIDs(ctx, req.OrgId)
			if err != nil {
				return err
			}
			for _, installationID := range installations {
				if err := s.uninstallSolutionTx(ctx, actorID, actorType, req.OrgId, installationID); err != nil {
					return w.Wrapf(err, "cannot uninstall %s", installationID)
				}
			}
			if err := s.revokeSourceDelegationsTx(ctx, actorID, SourceDelegationFilter{OrgID: req.OrgId}, SourceDelegationMemberRemoved); err != nil {
				return err
			}
			if _, err := s.store.RemoveAllOrgMembers(ctx, req.OrgId); err != nil {
				return err
			}
			if err := s.store.ArchiveOrganization(ctx, req.OrgId, actorID); err != nil {
				return err
			}
			return s.emitTx(ctx, actorID, actorType, EventOrgDeleted, "organization", req.OrgId, req.OrgId, map[string]any{
				"slug": org.Slug,
			})
		}); err != nil {
		return w.Wrapf(err, "cannot delete organization")
	}
	for _, member := range members {
		_ = s.invalidateMembership(ctx, req.OrgId, member.UserId)
	}
	return nil
}

// ListAllOrganizations is the platform view of every organization. Offset
// pagination behind an opaque token: the listing is an operator's, small, and
// ordered by name.
func (s *Service) ListAllOrganizations(ctx context.Context, actorID string, req *gen.ListAllOrganizationsRequest) (*gen.ListAllOrganizationsResponse, error) {
	w := wool.Get(ctx).In("ListAllOrganizations")
	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, err
	}
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	offset := 0
	if req.PageToken != "" {
		parsed, err := strconv.Atoi(req.PageToken)
		if err != nil || parsed < 0 {
			return nil, NewStoreError(fmt.Errorf("invalid page token"), ErrTypeValidation)
		}
		offset = parsed
	}
	var page []*gen.PlatformOrganization
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		page, err = s.store.ListAllOrganizations(ctx, req.Query, req.IncludeArchived, pageSize+1, offset)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list organizations")
	}
	response := &gen.ListAllOrganizationsResponse{Organizations: page}
	if len(page) > pageSize {
		response.Organizations = page[:pageSize]
		response.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	return response, nil
}

// GetOrganizationRoster lists any organization's members for the platform view.
func (s *Service) GetOrganizationRoster(ctx context.Context, actorID string, req *gen.GetOrganizationRosterRequest) (*gen.GetOrganizationRosterResponse, error) {
	w := wool.Get(ctx).In("GetOrganizationRoster")
	if err := s.requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, err
	}
	var members []*gen.OrgMembership
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		members, err = s.store.ListOrgMembers(ctx, req.OrgId)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list organization members")
	}
	return &gen.GetOrganizationRosterResponse{Members: members}, nil
}

// IssueInvitationLink rotates a pending invitation's token and hands its accept
// link to the administrator, without email. Every earlier link — an emailed one
// included — stops working, which is what makes "copy the link again" safe.
func (s *Service) IssueInvitationLink(ctx context.Context, actorID string, req *gen.IssueInvitationLinkRequest, orgID string) (*gen.IssueInvitationLinkResponse, error) {
	w := wool.Get(ctx).In("IssueInvitationLink")
	plaintext, tokenHash, err := newInvitationToken()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate invitation credential")
	}
	acceptURL, err := s.invitationAcceptURL(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	var inv *Invitation
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		fresh, err := s.store.GetInvitationByID(ctx, req.Id)
		if err != nil || fresh == nil {
			return ErrInvitationUnavailable
		}
		if fresh.Status != "pending" {
			return ErrInvitationUnavailable
		}
		fresh.TokenHash = tokenHash
		fresh.ExpiresAt = time.Now().UTC().Add(invitationTTL)
		if err := s.store.RotateInvitationToken(ctx, fresh); err != nil {
			return err
		}
		inv = fresh
		return s.emitTx(ctx, actorID, "user", EventInvitationLinkIssued, "invitation", inv.ID, orgID)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot issue invitation link")
	}
	return &gen.IssueInvitationLinkResponse{Invitation: invitationToProto(inv), AcceptUrl: acceptURL}, nil
}
