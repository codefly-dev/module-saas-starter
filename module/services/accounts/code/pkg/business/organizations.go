package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ErrOrgAdminContinuity reports a membership change that would strip an
// organization of its last administrative membership. Removal and demotion
// are the same violation and return the same error.
var ErrOrgAdminContinuity = errors.New("organization must keep at least one owner or admin")

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

// OrgAdministrationLockKey names the transaction-scoped advisory lock that
// serializes one organization's administrative standing. Every writer of
// organization_members takes it — the business service through
// Store.LockOrgAdministration, the pre-authentication identity resolver
// directly on its own transaction — so the key is stated once here rather than
// spelled out at each of them.
func OrgAdministrationLockKey(orgID string) string {
	return "administration:" + orgID
}

// IsOrgAdminRole reports whether a role carries organization administrative
// authority. Both writers of organization_members decide with it, so the set of
// administrative roles is stated once.
func IsOrgAdminRole(role string) bool {
	return role == "owner" || role == "admin"
}

// OrgAdminContinuity is the administrative-continuity invariant, applied to
// counted eligible administrators: current is how many the organization has
// now, projected how many it would have after the change.
//
// An organization that has no eligible administrator to begin with is exempt.
// The rule refuses to remove the last administrator; it does not refuse to
// operate on an organization that already has none, which would leave such data
// unrepairable — including by an operator adding an administrator back.
func OrgAdminContinuity(current int, projected int) error {
	if projected > 0 || current == 0 {
		return nil
	}
	return ErrOrgAdminContinuity
}

// OrgAdministration is one organization an identity administers: how many
// eligible administrators it has in total — the identity itself included, since
// it is one of them — and how many members other than the identity are still
// able to authenticate.
type OrgAdministration struct {
	OrgID                  string
	EligibleAdministrators int
	OtherActiveMembers     int
}

// StrandedByDeactivating reports whether deactivating the identity would leave
// this organization unadministrable *to somebody*. Two conditions, and the
// second is not a softening of the rule but the whole point of it: an
// organization nobody else is in has nobody to strand.
//
// RegisterUser gives every identity a personal organization it solely owns, so
// an organization count alone would refuse every deletion on this platform —
// including the ordinary member's, who administers nothing anyone shares.
// What the invariant protects is the members who would be left in an
// organization no one can administer, so it asks whether there are any.
func (a OrgAdministration) StrandedByDeactivating() bool {
	if a.OtherActiveMembers == 0 {
		return false
	}
	// The identity is one of the counted administrators, so deactivating it
	// removes exactly one.
	return OrgAdminContinuity(a.EligibleAdministrators, a.EligibleAdministrators-1) != nil
}

// ErrIdentityAdminContinuity reports a deactivation refused because the identity
// is the only eligible administrator of at least one organization. It is the
// same invariant ErrOrgAdminContinuity guards, reached from the identity's side
// rather than one organization's, and it is a distinct sentinel because the
// remedy is too: administration of those organizations has to be handed over
// first, and the error names them.
var ErrIdentityAdminContinuity = errors.New("identity is the only administrator of an organization")

// IdentityAdminContinuityError carries the organizations a refused deactivation
// would strand, so the caller is told which handovers it is waiting on instead
// of only that something is.
type IdentityAdminContinuityError struct {
	Organizations []string
}

func (e *IdentityAdminContinuityError) Error() string {
	return fmt.Sprintf("%s: %s", ErrIdentityAdminContinuity, strings.Join(e.Organizations, ", "))
}

func (e *IdentityAdminContinuityError) Unwrap() error { return ErrIdentityAdminContinuity }

// maxDeactivationLockPasses bounds the enumerate-and-lock loop below. Each pass
// past the second means the identity gained an administrative membership while
// the loop was running; more than a handful of those in one transaction is not
// contention, it is something pathological, and failing loudly beats looping.
const maxDeactivationLockPasses = 8

// organizationsStrandedByDeactivation locks the administrative standing of
// every organization the identity administers and returns those its
// deactivation would strand — see OrgAdministration.StrandedByDeactivating for
// what that means.
//
// A deactivation is not a membership change and cannot be expressed as one: it
// leaves every organization_members row standing and removes the identity from
// all of them at once, so the invariant has to be evaluated per organization
// and all of those organizations have to be pinned at the same time. Callers
// hold the locks for the rest of the transaction that writes the status, which
// is what serializes a deactivation against a concurrent removal or demotion in
// any of the same organizations.
//
// The loop is the load-bearing part, and one pass is not enough. A promotion
// can only raise an administrator count, so AddOrgMember settles it from its
// argument and takes no administration lock — which means the identity can be
// made an administrator of an organization this loop has already read past.
// Deciding on an organization whose lock is not held admits exactly the
// interleaving the lock exists to stop: the promotion lands, a demotion in that
// same organization counts this identity (still active, because this
// transaction has not committed) and commits, and then the deactivation commits
// on top and leaves the organization with nobody. So the loop re-reads until
// the roster comes back unchanged with every organization in it already locked,
// and only then decides.
//
// Organizations are locked in ascending id, and the order is established here
// rather than trusted from the query, because it is this loop that depends on
// it: it is what keeps two concurrent deactivations over shared organizations
// queueing instead of waiting on each other. A pass that discovers a new
// organization sorting below one already held does acquire out of that order —
// unavoidable, since a transaction-scoped advisory lock cannot be released to
// retake it — so two deactivations that discover each other's organizations
// mid-loop can deadlock. PostgreSQL detects that and aborts one with a loud
// serialization error; it is a far better failure than the silent stranding it
// replaces, and it needs a promotion to land inside both loops to happen at all.
func (s *Service) organizationsStrandedByDeactivation(ctx context.Context, userID string) ([]string, error) {
	w := wool.Get(ctx).In("organizationsStrandedByDeactivation")

	held := map[string]bool{}
	var previous []string
	for range maxDeactivationLockPasses {
		administered, err := s.store.ListAdministeredOrganizations(ctx, userID)
		if err != nil {
			return nil, w.Wrapf(err, "cannot list administered organizations")
		}
		orgs := make([]string, 0, len(administered))
		for _, administration := range administered {
			orgs = append(orgs, administration.OrgID)
		}
		slices.Sort(orgs)

		// previous is nil only on the first pass, which has taken no locks yet,
		// so an identity that administers nothing still gets a second read
		// rather than a verdict from an unlocked one.
		if previous != nil && slices.Equal(orgs, previous) {
			var stranded []string
			for _, administration := range administered {
				if administration.StrandedByDeactivating() {
					stranded = append(stranded, administration.OrgID)
				}
			}
			return stranded, nil
		}

		for _, orgID := range orgs {
			if held[orgID] {
				continue
			}
			if err := s.store.LockOrgAdministration(ctx, orgID); err != nil {
				return nil, w.Wrapf(err, "cannot lock organization administration")
			}
			held[orgID] = true
		}
		previous = orgs
	}
	return nil, w.NewError(
		"the organizations this identity administers kept changing under their administration locks")
}

// requireOrgAdminContinuity takes the organization's administration lock and
// evaluates the proposed post-change state against it. The lock is the point:
// the roster read alone is not serialized, so two transactions can each observe
// two administrators and each remove one. Callers hold it for the rest of their
// transaction and write the membership row under it.
//
// Lock order for anything that takes more than one of these:
// LockOrgAdministration -> LockOrgMembership -> LockEntitlementQuota.
func (s *Service) requireOrgAdminContinuity(ctx context.Context, orgID string, userID string, role string) error {
	w := wool.Get(ctx).In("requireOrgAdminContinuity")

	// A change that leaves the target holding administrative authority cannot
	// reduce the count, so it can never violate the invariant. This is a
	// property of the argument, not of concurrently mutable state, so it is safe
	// to decide before taking the lock — and it keeps promotions and
	// administrator invitations off the organization-wide serialization point
	// entirely. Not holding the lock here can only make a concurrent check miss
	// this new administrator and be more conservative, never less.
	if IsOrgAdminRole(role) {
		return nil
	}

	if err := s.store.LockOrgAdministration(ctx, orgID); err != nil {
		return w.Wrapf(err, "cannot lock organization administration")
	}
	// role is non-administrative here, so the administrators that would remain
	// are exactly those held by somebody other than the target.
	current, others, err := s.store.CountOrgAdministrators(ctx, orgID, userID)
	if err != nil {
		return w.Wrapf(err, "cannot count organization administrators")
	}
	return OrgAdminContinuity(current, others)
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

// AddOrgMember adds a member to an organization, or updates the role of one
// already there. The seat decision and membership write share one tenant
// transaction and one per-org quota lock, so concurrent admin requests cannot
// consume the same final seat. Updating an existing member remains idempotent
// even when the organization is full.
//
// The upsert is a role change as much as an addition, so it carries the same
// administrative-continuity invariant RemoveOrgMember does: an update that
// demotes the last owner or admin is rejected, not admitted because it happens
// to reuse the add path.
func (s *Service) AddOrgMember(ctx context.Context, actorID string, req *gen.AddOrgMemberRequest) error {
	w := wool.Get(ctx).In("AddOrgMember")

	role := orgRoleToString(req.Role)
	var orgName string
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		// Administration lock before the quota lock — that order is the one
		// documented in AUTHZ.md, and this is the only path that holds both.
		if err := s.requireOrgAdminContinuity(ctx, req.OrgId, req.UserId, role); err != nil {
			return err
		}
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
//
// Administrative continuity is not among the bypasses: seeding only ever adds,
// so a well-formed fixture never sees the rule, and one that would demote an
// organization's last administrator should fail the boot rather than converge
// an organization nobody can administer.
func (s *Service) ConvergeFixtureOrgMember(ctx context.Context, req *gen.AddOrgMemberRequest) error {
	w := wool.Get(ctx).In("ConvergeFixtureOrgMember")
	role := orgRoleToString(req.Role)
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		if err := s.requireOrgAdminContinuity(ctx, req.OrgId, req.UserId, role); err != nil {
			return err
		}
		return s.store.AddOrgMember(ctx, req.OrgId, req.UserId, role)
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
//   - Administrative continuity: if the target is the only remaining
//     owner/admin, reject. Otherwise we'd leave the org with no one who can
//     manage it. Same invariant, same error as a demotion through AddOrgMember.
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
	// Two locks, in the order AUTHZ.md fixes. The organization-wide
	// administration lock is taken first, inside the continuity guard, because
	// the count it reads is an organization-wide fact. The (org, user) lock is
	// taken before the writes, so a concurrent team insert for the same pair
	// either commits before the dependent delete or waits behind this
	// transaction, and can neither be missed by it nor land after it.
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		if err := s.requireOrgAdminContinuity(ctx, req.OrgId, req.UserId, ""); err != nil {
			return err
		}
		if err := s.store.LockOrgMembership(ctx, req.OrgId, req.UserId); err != nil {
			return w.Wrapf(err, "cannot lock org membership")
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
