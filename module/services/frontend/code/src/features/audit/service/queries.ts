import { useQuery } from "@tanstack/react-query";
import { useAuditService } from "@/lib/hooks/use-api-client";
import { toAuditEvent } from "../model/transforms";
import type { AuditEventTypeInfo, AuditLogFilters } from "../model/types";

export function useAuditLog(
	params: AuditLogFilters,
	options: { enabled?: boolean } = {},
) {
	const svc = useAuditService();
	return useQuery({
		queryKey: ["audit-log", params],
		queryFn: () =>
			svc.queryAuditLog({
				orgId: params.orgId ?? "",
				eventType: params.eventType ?? "",
				category: params.category ?? "",
				actorId: params.actorId ?? "",
				pageSize: params.pageSize ?? 50,
			}),
		enabled: options.enabled,
		select: (data) => ({
			events: data.events.map(toAuditEvent),
			totalCount: data.totalCount,
		}),
	});
}

// useAuditEventTypes fetches the server-owned registry so the filter facet is a
// projection of the catalog rather than a hand-maintained list.
export function useAuditEventTypes(options: { enabled?: boolean } = {}) {
	const svc = useAuditService();
	return useQuery({
		queryKey: ["audit-event-types"],
		queryFn: () => svc.listAuditEventTypes({}),
		enabled: options.enabled,
		staleTime: 5 * 60 * 1000,
		select: (data): AuditEventTypeInfo[] =>
			data.types.map((t) => ({
				name: t.name,
				version: t.version,
				category: t.category,
				owner: t.owner,
				deprecated: t.deprecated,
				description: t.description,
			})),
	});
}

export interface AuditAggregateParams {
	orgId?: string;
	eventType?: string;
	category?: string;
	groupBy: "event_type" | "category" | "actor" | "time";
	bucket?: "day" | "week" | "month";
}

export function useAuditAggregate(
	params: AuditAggregateParams,
	options: { enabled?: boolean } = {},
) {
	const svc = useAuditService();
	return useQuery({
		queryKey: ["audit-aggregate", params],
		queryFn: () =>
			svc.aggregateAuditLog({
				orgId: params.orgId ?? "",
				eventType: params.eventType ?? "",
				category: params.category ?? "",
				groupBy: params.groupBy,
				bucket: params.bucket ?? "",
			}),
		enabled: options.enabled,
		select: (data) => data.buckets.map((b) => ({ key: b.key, count: Number(b.count) })),
	});
}
