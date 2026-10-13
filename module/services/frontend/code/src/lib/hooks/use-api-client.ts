import { createAccountsClients } from "@/gen/saas/accounts/v1/frontend_catalog";
import { apiTransport } from "@/lib/connect/transport";

const clients = createAccountsClients(apiTransport);

export function useUserService() {
	return clients.UserService;
}

export function useOrganizationService() {
	return clients.OrganizationService;
}

export function useTeamService() {
	return clients.TeamService;
}

export function usePermissionService() {
	return clients.PermissionService;
}

export function usePrincipalService() {
	return clients.PrincipalService;
}

export function useAuthService() {
	return clients.AuthService;
}

export function useAuditService() {
	return clients.AuditService;
}

export function useAPIKeyService() {
	return clients.APIKeyService;
}

export function usePlatformAdminService() {
	return clients.PlatformAdminService;
}

export function useInvitationService() {
	return clients.InvitationService;
}

export function useInstallationService() {
	return clients.InstallationService;
}

// The data-source surface, for the host's own reads of what keeps a source
// current. The source LIST and its controls come from the published kit's own
// client; this is the host asking about facts the kit carries no shape for —
// the trigger a source declares and the identity its occurrences run under.
export function useDatasourceService() {
	return clients.DatasourceService;
}
