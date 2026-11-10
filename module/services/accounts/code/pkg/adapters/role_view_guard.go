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
	policyv1 "accounts/pkg/gen/saas/policy/v1"
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

// roleViewOf returns what the session a call is made from views as, empty when
// it views nothing. A call that carries no session, or comes from a host with no
// authority core, has no view.
func roleViewOf(ctx context.Context) (domain.RoleView, error) {
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok || identity.SessionID == uuid.Nil || service == nil {
		return domain.RoleView{}, nil
	}
	core, err := service.AuthorityCore()
	if errors.Is(err, business.ErrAuthorityCoreUnconfigured) {
		return domain.RoleView{}, nil // a host that builds no core has no views
	}
	if err != nil {
		// A core that cannot be read right now must not open the restricted
		// surface to a session that may be a view.
		return domain.RoleView{}, status.Errorf(codes.Unavailable, "cannot read whether this session views as some of its roles: %v", err)
	}
	view, err := core.RoleViewOf(ctx, identity.SessionID.String())
	if errors.Is(err, domain.ErrNotFound) {
		return domain.RoleView{}, nil
	}
	if err != nil {
		return domain.RoleView{}, status.Errorf(codes.Unavailable, "cannot read whether this session views as some of its roles: %v", err)
	}
	return view, nil
}

// refuseInRoleView refuses the call when the session it is made from views as
// some of its roles.
func refuseInRoleView(ctx context.Context) error {
	view, err := roleViewOf(ctx)
	if err != nil {
		return err
	}
	if len(view.Roles) == 0 {
		return nil
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

// SwitchOrganization is deliberately not here: it re-signs the access token for another
// organisation and the new token would not be a view's, so a view refuses it by name.
//
// The organization-role helpers (requireOrgAdmin, requireOrgOwner, requireOrgPermission,
// requireBillingAdmin) call refuseInRoleView first: an organization role is the
// person's own, not the viewed roles', so a session that views holds none of it.
// They are the one place accounts decides by organization role, so the declared
// reads that a view admits (the invitation list, the installation list, the SSO
// configuration, usage) cannot show an administrator what a viewer cannot.
//
// roleViewWrites are the accounts writes a session may make while viewing as
// some of its roles, because they act on the person's own account or content and
// widen no authority. Everything else that is not a read is refused in a view.
// The list is closed on purpose: a new operation is refused until someone names
// it here, which is the opposite of the failure the review found (an operation
// that mints or administers is refused only if somebody remembered to say so).
var roleViewWrites = map[string]bool{
	"AuthService/Logout":         true,
	"ConsentService/AcceptTerms": true, "UserSettingsService/Update": true, "ConsentService/UpdatePreferences": true,
	"DashboardService/CreateDashboard": true, "DashboardService/UpdateDashboard": true, "DashboardService/DeleteDashboard": true,
	"NotificationService/MarkRead": true, "NotificationService/MarkAllRead": true, "NotificationService/DeleteNotification": true,
	"ResourceFollowService/Follow": true, "ResourceFollowService/Unfollow": true,
	"OnboardingService/CompleteStep": true, "OnboardingService/SkipStep": true,
}

// roleViewReadsThatWrite are operations whose descriptor says STANDARD_READ but
// that decide something: DecideDelegation approves a delegation and mints from
// it. They are refused in a view whatever their rate-limit class says.
var roleViewReadsThatWrite = map[string]bool{"DelegationService/DecideDelegation": true, "DelegationService/WaitForDelegation": true}

// roleViewAdmits reports whether a session viewing as some of its roles may call
// an accounts operation. Default deny: only a declared read, a login-flow call
// that carries no session authority, or a named person-owned write passes.
func roleViewAdmits(fullMethod string, policy business.RPCPolicy, resolved bool) bool {
	if !resolved {
		return false
	}
	short := strings.TrimPrefix(fullMethod, "/saas.accounts.v1.")
	if roleViewReadsThatWrite[short] {
		return false
	}
	switch policy.MethodPolicy.GetRateLimit() {
	case policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_READ,
		policyv1.RateLimitClass_RATE_LIMIT_CLASS_PUBLIC,
		policyv1.RateLimitClass_RATE_LIMIT_CLASS_AUTHENTICATION:
		return !business.ImpersonationForbidden(policy) || short == "AuthService/Logout"
	}
	return roleViewWrites[short]
}

// enforceRoleViewPolicy refuses, in a role view, every operation of the accounts
// API that roleViewAdmits does not name. Those operations are decided by
// organisation roles (API keys, approvals, delegations, installs, platform and
// member administration, the person Work Context mints), which a role view does
// not narrow; the session the view was opened from performs them as before. The
// host interfaces (host.*) are exempt because the authority core decides them
// and reads the view itself; TestEveryPersonMintEntryPointHonoursOrRefusesARoleView
// holds each of those to a stated decision.
func enforceRoleViewPolicy(ctx context.Context, fullMethod string) error {
	if !strings.HasPrefix(fullMethod, "/saas.accounts.v1.") {
		return nil
	}
	policy, resolved := business.LookupRPCPolicy(fullMethod)
	if roleViewAdmits(fullMethod, policy, resolved) {
		return nil
	}
	return refuseInRoleView(ctx)
}
