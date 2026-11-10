package infra

import (
	"context"
	"fmt"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Platform read authority is the one exception to "only a grant or a share
// confers read" in the layered-access oracles: a platform super_admin reads
// every scope node — every collection and every record placed under one — in
// any organization, without a grant.
//
// Every read decision the host makes resolves through this file: CheckAccess,
// the accessible-scope listing and its point and candidate checks, and the
// module-facing readable-source projection. Keeping the rule here, beside the
// grant and share branches, is what makes the host UI, a module acting for a
// viewer, and the fan-out paths give one answer.
//
// It is deliberately narrow, and fails closed on every other axis:
//
//   - only the `super_admin` platform role — not support or billing, and not
//     an organization owner or administrator, whose flat role has never
//     substituted for a collection grant;
//   - only the `read` action, spelled exactly — a wildcard action names no
//     action a caller can hold this way;
//   - only a principal subject, never a team;
//   - never on an impersonated request. Neither party's platform role reaches
//     it: the effective subject's grants are not the impersonator's to borrow,
//     and the impersonator's own are left behind when they step into someone
//     else's session. This mirrors the adapters' platformRole lookup, the gate
//     every other platform check shares.
//
// A Work Context cannot be minted while impersonating (every mint RPC is
// IMPERSONATION_REQUIREMENT_FORBIDDEN), so a module presenting one always names
// a subject that signed in as themselves, and the owner's platform role applies
// to it exactly as it does to that person's own request.
const platformReadRole = "super_admin"

// PlatformReadBasis is the reason CheckAccess reports when a record is readable
// only because the subject is a platform administrator, so the decision says it
// was not a grant.
const PlatformReadBasis = "granted via platform_administrator"

// platformReadAdmissible reports whether a platform administrator's authority
// may enter this decision at all. It does not say the subject holds the role —
// the query answers that against platform_admins in the same snapshot as the
// grants it is an alternative to.
func platformReadAdmissible(ctx context.Context, subjectKind gen.SubjectKind, action string) bool {
	return subjectKind == gen.SubjectKind_SUBJECT_KIND_PRINCIPAL &&
		action == "read" &&
		!auth.ImpersonatedRequest(ctx)
}

// platformReadPredicate is the SQL half: admissible (a bound boolean) AND the
// subject expression names a current platform super_admin. The EXISTS is
// uncorrelated with the node being tested, so the planner evaluates it once.
func platformReadPredicate(admissibleParam, subjectExpr string) string {
	return fmt.Sprintf(`(%s::bool AND EXISTS (
		SELECT 1 FROM platform_admins pa
		WHERE pa.user_id = %s AND pa.platform_role = '%s'))`,
		admissibleParam, subjectExpr, platformReadRole)
}
