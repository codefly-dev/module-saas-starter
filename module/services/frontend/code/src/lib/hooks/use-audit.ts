import { useQuery } from "@tanstack/react-query";
import { useAuditService } from "./use-api-client";

export interface AuditLogParams {
	orgId?: string;
	action?: string;
	actorId?: string;
	pageSize?: number;
}

export function useAuditLog(params: AuditLogParams) {
	const svc = useAuditService();
	return useQuery({
		queryKey: ["audit-log", params],
		queryFn: () =>
			svc.queryAuditLog({
				orgId: params.orgId ?? "",
				action: params.action ?? "",
				actorId: params.actorId ?? "",
				pageSize: params.pageSize ?? 50,
			}),
		select: (data) => ({
			events: data.events,
			totalCount: data.totalCount,
		}),
	});
}
