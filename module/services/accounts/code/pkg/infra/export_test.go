package infra

// IdentityScopeProbe exposes the scope query ListAdministeredOrganizations runs,
// so a test can assert what admits a control-plane transaction. Deciding on the
// role attribute rather than the assumed role is invisible on a profile that
// grants BYPASSRLS and fatal on the managed one, which grants none.
const IdentityScopeProbe = identityScopeProbe

// ControlPlaneDatabaseRole is the role withControlPlaneTx assumes.
const ControlPlaneDatabaseRole = controlPlaneDatabaseRole

// UseMemberPermissions replaces the generated catalog's member grant for one
// test, so the SQL branch is exercised without a composed catalog.
func UseMemberPermissions(held func(resource, action string) bool) (restore func()) {
	previous := isMemberPermission
	isMemberPermission = held
	return func() { isMemberPermission = previous }
}
