import { useQuery } from "@tanstack/react-query";
import { useAuditService } from "@/lib/hooks/use-api-client";
import type { AuditLogFilters } from "../model/types";

export function useAuditLog(params: AuditLogFilters) {
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
