import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Client } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import type {
	AggregateAuditLogRequestSchema,
	AggregateAuditLogResponse,
	AuditService,
} from "@/gen/saas/accounts/v1/audit_pb";
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

// auditEventTypesQuery is the single react-query descriptor for the server-owned
// registry, shared by the hook and by imperative readers (the authoring surface)
// so both resolve the same key, staleTime, and projection — one cache, one
// invalidation surface. The projection lives in `queryFn`, not `select`:
// `queryClient.fetchQuery` does not apply `select` (react-query v5), so an
// imperative reader must get the already-projected `AuditEventTypeInfo[]` from
// the cached value itself.
export const auditEventTypesQuery = (
	svc: Pick<Client<typeof AuditService>, "listAuditEventTypes">,
) => ({
	queryKey: ["audit-event-types"] as const,
	queryFn: async (): Promise<AuditEventTypeInfo[]> => {
		const data = await svc.listAuditEventTypes({});
		return data.types.map((t) => ({
			name: t.name,
			version: t.version,
			category: t.category,
			owner: t.owner,
			deprecated: t.deprecated,
			description: t.description,
		}));
	},
	staleTime: 5 * 60 * 1000,
});

// useAuditEventTypes fetches the server-owned registry so the filter facet is a
// projection of the catalog rather than a hand-maintained list.
export function useAuditEventTypes(options: { enabled?: boolean } = {}) {
	const svc = useAuditService();
	return useQuery({ ...auditEventTypesQuery(svc), enabled: options.enabled });
}

// A group dimension is one of the fixed keys or a payload field addressed as
// `payload:<key>`.
export type AuditGroupDimension =
	| "event_type"
	| "category"
	| "actor"
	| "time"
	| `payload:${string}`;

export type AuditMetricOp =
	| "count"
	| "count_distinct"
	| "sum"
	| "avg"
	| "min"
	| "max"
	| "percentile";

export interface AuditMetricSpec {
	op: AuditMetricOp;
	// Required for every op except count. Numeric ops need a `payload:<key>`.
	field?: string;
	// Used only when op === "percentile" (0.95 → p95).
	percentile?: number;
	// Names the metric in each bucket's `metrics` map; defaults server-side.
	alias?: string;
}

export interface AuditDerivedSpec {
	alias: string;
	numerator: string;
	denominator: string;
}

export interface AuditAggregateParams {
	orgId?: string;
	eventType?: string;
	category?: string;
	// groupBy is the sole dimension; groupBys supersedes it for multi-dim
	// grouping. One of the two should be set.
	groupBy?: AuditGroupDimension;
	groupBys?: AuditGroupDimension[];
	bucket?: "day" | "week" | "month";
	// from/to bound the audit window; omit for all-time.
	from?: Date;
	to?: Date;
	metrics?: AuditMetricSpec[];
	derived?: AuditDerivedSpec[];
}

// AuditAggregateBucket is the client-side shape of one aggregation row: the
// group keys plus every metric/derived value keyed by alias. `key`/`count`
// mirror `keys[0]` and the group's COUNT(*) for count-only callers.
export interface AuditAggregateBucket {
	key: string;
	count: number;
	keys: string[];
	metrics: Record<string, number>;
}

// Bind the client-side aggregate params to the wire request: fill defaults,
// convert the Date window to timestamps, and normalize the metric/derived
// specs. Shared so an imperative caller (e.g. the authoring preview) issues the
// exact same query the hook does.
export function toAggregateRequest(
	params: AuditAggregateParams,
): MessageInitShape<typeof AggregateAuditLogRequestSchema> {
	return {
		orgId: params.orgId ?? "",
		eventType: params.eventType ?? "",
		category: params.category ?? "",
		groupBy: params.groupBy ?? "",
		groupBys: params.groupBys ?? [],
		bucket: params.bucket ?? "",
		from: params.from ? timestampFromDate(params.from) : undefined,
		to: params.to ? timestampFromDate(params.to) : undefined,
		metrics: (params.metrics ?? []).map((m) => ({
			op: m.op,
			field: m.field ?? "",
			percentile: m.percentile ?? 0,
			alias: m.alias ?? "",
		})),
		derived: (params.derived ?? []).map((d) => ({
			alias: d.alias,
			numerator: d.numerator,
			denominator: d.denominator,
		})),
	};
}

// Shape a raw aggregate response into the client-side buckets: int64 counts and
// double metric values become numbers. Shared with the hook's `select`.
export function toAggregateBuckets(
	response: AggregateAuditLogResponse,
): AuditAggregateBucket[] {
	return response.buckets.map((b) => ({
		key: b.key,
		count: Number(b.count),
		keys: b.keys,
		metrics: Object.fromEntries(
			Object.entries(b.metrics).map(([k, v]) => [k, Number(v)]),
		),
	}));
}

export function useAuditAggregate(
	params: AuditAggregateParams,
	options: { enabled?: boolean } = {},
) {
	const svc = useAuditService();
	return useQuery({
		queryKey: ["audit-aggregate", params],
		queryFn: () => svc.aggregateAuditLog(toAggregateRequest(params)),
		enabled: options.enabled,
		select: toAggregateBuckets,
	});
}
