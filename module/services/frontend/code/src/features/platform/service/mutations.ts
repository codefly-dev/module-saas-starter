import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Entitlement } from "@/gen/saas/accounts/v1/frontend_catalog";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

export function useGrantPlatformRole() {
	const svc = usePlatformAdminService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			userId,
			platformRole,
		}: {
			userId: string;
			platformRole: string;
		}) => svc.grantPlatformRole({ userId, platformRole }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["platform-admins"] }),
	});
}

export function useRevokePlatformRole() {
	const svc = usePlatformAdminService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (userId: string) => svc.revokePlatformRole({ userId }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["platform-admins"] }),
	});
}

export function useOverrideEntitlement() {
	const svc = usePlatformAdminService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			orgId,
			feature,
			limitValue,
			reason,
		}: {
			orgId: string;
			feature: Entitlement;
			limitValue: bigint;
			reason?: string;
		}) =>
			svc.overrideEntitlement({
				orgId,
				feature,
				limitValue,
				reason: reason ?? "",
			}),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["entitlements"] }),
	});
}

export function useImpersonateUser() {
	const svc = usePlatformAdminService();
	return useMutation({
		mutationFn: (userId: string) => svc.impersonateUser({ userId }),
	});
}

export function useRevokeSession() {
	const svc = usePlatformAdminService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			sessionId,
			reason,
		}: {
			sessionId: string;
			reason?: string;
		}) => svc.revokeSession({ sessionId, reason: reason ?? "" }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
	});
}
