package infra

// IdentityScopeProbe exposes the scope query ListAdministeredOrganizations runs,
// so a test can assert what admits a control-plane transaction. Deciding on the
// role attribute rather than the assumed role is invisible on a profile that
// grants BYPASSRLS and fatal on the managed one, which grants none.
const IdentityScopeProbe = identityScopeProbe

// ControlPlaneDatabaseRole is the role withControlPlaneTx assumes.
const ControlPlaneDatabaseRole = controlPlaneDatabaseRole
