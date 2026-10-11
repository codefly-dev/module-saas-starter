package business

import (
	"context"
	"errors"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrSubjectOutsideOrganization refuses a write that names, as the subject of
// authority in one organization, a principal or team that does not belong to
// it. It is one answer for every way a subject can fail to belong — another
// organization's, nonexistent, or the wrong kind — so the refusal is never a
// way to learn another organization's roster.
var ErrSubjectOutsideOrganization = status.Error(codes.PermissionDenied, "subject does not belong to this organization")

// canonicalSubjectTenancyIDs keeps the lock, tenant scope, lookups and writes
// on the same UUID values, regardless of the spelling supplied by a caller.
func canonicalSubjectTenancyIDs(orgID, subjectID string) (string, string, error) {
	org, err := ParseID(orgID)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, "invalid organization id")
	}
	subject, err := ParseID(subjectID)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, "invalid subject id")
	}
	return org.String(), subject.String(), nil
}

// requireSubjectInOrgTx settles, inside the caller's organization transaction,
// that the subject a write names belongs to orgID — so the check and the write
// it guards describe one moment.
//
//   - A principal that is a member is held there by the same (org, user) lock a
//     membership removal takes (removeOrgMembershipTx), taken before the
//     membership is read. A removal that commits first leaves no membership to
//     find; one that starts after waits for this transaction. A write can
//     therefore never land for a member who had already left.
//   - A principal that is not a member belongs only if its own row names the
//     organization (an agent or service principal), and is not revoked.
//   - A team belongs if the organization owns it.
//
// Run it before any write in the transaction, so the membership lock is taken
// before any trigger-acquired lock and the lock order stays the one removal uses.
func (s *Service) requireSubjectInOrgTx(ctx context.Context, orgID, subjectID string, kind gen.SubjectKind) error {
	orgID, subjectID, err := canonicalSubjectTenancyIDs(orgID, subjectID)
	if err != nil {
		return err
	}
	switch kind {
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		if err := s.store.LockOrgMembership(ctx, orgID, subjectID); err != nil {
			return err
		}
		member, err := s.store.OrgMemberExists(ctx, orgID, subjectID)
		if err != nil {
			return err
		}
		if member {
			return nil
		}
		principal, err := s.principalStore().GetPrincipal(ctx, subjectID)
		if err != nil {
			var se *StoreError
			if errors.As(err, &se) && se.StoreErrorType == ErrTypeNotFound {
				return ErrSubjectOutsideOrganization
			}
			return err
		}
		if principal == nil || principal.OrgID != orgID || principal.IsRevoked() {
			return ErrSubjectOutsideOrganization
		}
		return nil
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		teamOrgID, err := s.store.GetTeamOrgID(ctx, subjectID)
		if err != nil {
			return err
		}
		if teamOrgID != orgID {
			return ErrSubjectOutsideOrganization
		}
		return nil
	default:
		return status.Error(codes.InvalidArgument, "subject_kind must name a principal or a team")
	}
}
