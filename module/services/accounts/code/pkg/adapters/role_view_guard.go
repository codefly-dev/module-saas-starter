package adapters

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
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
	if !ok || identity.SessionID.String() == "" || service == nil {
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
		"this session views as %s; a person's own Work Contexts are decided by organization roles, which a role view does not narrow, so they are not minted in a role view: use the session the view was opened from",
		strings.Join(viewed, "; "))
}
