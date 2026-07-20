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
