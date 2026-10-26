package adapters

import "accounts/pkg/gen/saas/accounts/v1/accountsv1connect"

// Only the directory's three reads accept the forwarded capability. The
// organization and team administrative services keep their bearer admission.
func directoryProcedure(procedure string) bool {
	switch procedure {
	case accountsv1connect.DirectoryServiceListOrganizationMembersProcedure,
		accountsv1connect.DirectoryServiceListTeamsProcedure,
		accountsv1connect.DirectoryServiceListTeamMembersProcedure:
		return true
	default:
		return false
	}
}
