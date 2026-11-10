package adapters

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
)

// A role view fixes a session to act as some of the roles its person holds on
// an installation. The authority core honours it wherever it reads roles, so
// every capability the host Issue decides (IssueForPerson, a page's hop, a
// delegation, the menu, role administration) is already the view's. These
// mints are the exception: a person's own Work Context mints here
// (WorkContextService's StartTask, StartRootSession, ExchangeAudience and
// StartChildSession, all behind authorizeOwner) are decided by organization
// RBAC, which a role view does not narrow, so a view session that minted here
// would hold the editor's authority behind a page that looks like a viewer's.
// They are refused by name instead, for any session that views; the session the
// view was opened from mints them as before. The mints a person's session
// reaches through other entry points are listed in
// TestEveryPersonMintEntryPointHonoursOrRefusesARoleView, which fails when a
// method is added without a decision.

// refuseInRoleView refuses the call when the session it is made from views as
// some of its roles. A call that carries no session, or comes from a host with
// no authority core, has no view to refuse.
func refuseInRoleView(ctx context.Context) error {
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok || identity.SessionID == uuid.Nil || service == nil {
		return nil
	}
	core, err := authorityCore()
	if err != nil {
		return nil
	}
	view, err := core.RoleViewOf(ctx, identity.SessionID.String())
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot read whether this session views as some of its roles: %v", err)
	}
	var viewed []string
	for _, roles := range view.Roles {
		viewed = append(viewed, strings.Join(roles, ", "))
	}
	slices.Sort(viewed)
	return status.Errorf(codes.PermissionDenied,
		"this session views as %s; this operation is decided by organization roles, which a role view does not narrow, so it is unavailable in a role view: use the session the view was opened from",
		strings.Join(viewed, "; "))
}

// enforceRoleViewPolicy refuses, in a role view, every operation of the accounts
// API that the descriptors withhold from an impersonated session. Those are the
// privileged mutations (API keys, approvals, platform administration, person
// Work Context mints, organisation and member administration), and every one is
// decided by organisation roles, which a role view does not narrow; the session
// the view was opened from performs them as before. The host interfaces
// (host.*) are exempt because the authority core decides them and reads the
// view itself; TestEveryPersonMintEntryPointHonoursOrRefusesARoleView holds
// each of those to a stated decision. A new restricted accounts operation is
// therefore refused in a view by default, with nothing to remember.
func enforceRoleViewPolicy(ctx context.Context, fullMethod string) error {
	if !strings.HasPrefix(fullMethod, "/saas.accounts.v1.") {
		return nil
	}
	if policy, ok := business.LookupRPCPolicy(fullMethod); ok && !business.ImpersonationForbidden(policy) {
		return nil
	}
	return refuseInRoleView(ctx)
}
