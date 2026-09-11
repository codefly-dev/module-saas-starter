import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Client } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import type {
	AggregateAuditLogRequestSchema,
	AggregateAuditLogResponse,
	AuditService,
} from "@/gen/saas/accounts/v1/audit_pb";
import {
	useAuditService,
	usePrincipalService,
} from "@/lib/hooks/use-api-client";
import { toAuditEvent } from "../model/transforms";
import type {
	AuditEventTypeInfo,
	AuditLogFilters,
	PrincipalDirectory,
} from "../model/types";

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
				namespace: params.namespace ?? "",
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

// ListPrincipals has no id filter, so a name lookup walks the org's principals
// newest-first until every actor it was asked about is named. Pages are capped
// at 200 server-side; the walk stops at PRINCIPAL_PAGE_LIMIT pages, so an actor
// outside the 1000 most recently created principals stays unresolved and falls
// back to its truncated id. In practice an audit page's actors are recent and
// page one answers it, which is what keeps this off the dashboard's hot path.
const PRINCIPAL_PAGE_SIZE = 200;
const PRINCIPAL_PAGE_LIMIT = 5;

const EMPTY_DIRECTORY: PrincipalDirectory = new Map();

export interface PrincipalDirectoryResult {
	directory: PrincipalDirectory;
	// A failed walk is not an org with no names: without this the surface
	// renders every actor as an id and gives the reader nothing to act on.
	failed: boolean;
}

// usePrincipalDirectory names the actors in `actorIds` for the audit surfaces.
// The directory is empty until the walk lands, because every consumer already
// renders a fallback for an id it cannot resolve — an in-flight directory is
// just one more unresolved actor.
export function usePrincipalDirectory(
	orgId: string,
	actorIds: readonly string[],
): PrincipalDirectoryResult {
	const svc = usePrincipalService();
	// Sorted and deduped so the query key is stable across renders that pass an
	// equal-but-new array, and so it identifies the question being asked.
	const wanted = Array.from(new Set(actorIds.filter(Boolean))).sort();
	const { data, isError } = useQuery({
		queryKey: ["principal-directory", orgId, wanted.join(",")],
		queryFn: async (): Promise<PrincipalDirectory> => {
			const directory = new Map<string, string>();
			let pageToken = "";
			for (let page = 0; page < PRINCIPAL_PAGE_LIMIT; page++) {
				const res = await svc.listPrincipals({
					orgId,
					pageSize: PRINCIPAL_PAGE_SIZE,
					pageToken,
				});
				for (const p of res.principals) {
					directory.set(p.id, p.displayName);
				}
				if (!res.nextPageToken) break;
				if (wanted.every((id) => directory.has(id))) break;
				pageToken = res.nextPageToken;
			}
			return directory;
		},
		enabled: orgId !== "" && wanted.length > 0,
		staleTime: 5 * 60 * 1000,
	});
	return { directory: data ?? EMPTY_DIRECTORY, failed: isError };
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
			namespace: t.namespace,
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
	namespace?: string;
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
		namespace: params.namespace ?? "",
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
