import type { Client } from "@connectrpc/connect";
import type { AuditEventTypeInfo } from "@/features/audit";
import {
	toAggregateBuckets,
	toAggregateRequest,
} from "@/features/audit/service/queries";
import type { AuditService } from "@/gen/saas/accounts/v1/audit_pb";
import type { DashboardDef, MetricDef } from "../model/schema";
import {
	type AuditVocabulary,
	type FieldError,
	validateDashboard,
	validateMetric,
} from "../model/validate";
import {
	compileMetricQuery,
	type MetricPoint,
	shapeMetricSeries,
} from "./use-metric";

// EventTypeVocabulary is the driver-facing catalog: the registered event types
// (with their metadata, so a form can label them) plus the distinct categories
// they fall under. It is exactly what a driver needs to compose a metric that
// references something real. The event shape reuses the audit registry's own
// AuditEventTypeInfo — a single source of truth for that projection.
export interface EventTypeVocabulary {
	events: AuditEventTypeInfo[];
	categories: string[];
}

// MetricPreview is the resolved shape of a single metric: the same ordered
// points and total a mounted card would render, so a driver sees real data
// before committing.
export interface MetricPreview {
	points: MetricPoint[];
	total: number;
}

// A driver-facing operation returns either a value or the precise guard-rail
// failures that blocked it — never a bare throw for a spec it can fix.
export type PreviewResult =
	| { ok: true; preview: MetricPreview }
	| { ok: false; errors: FieldError[] };

export type CommitResult =
	| { ok: true; spec: DashboardDef }
	| { ok: false; errors: FieldError[] };

// DashboardAuthoring is the contract an external driver binds to: read the
// vocabulary, preview a metric against live audit data, and commit a spec
// through validation into the draft. Read-only against audit; the only write is
// the local draft, applied through the injected `commit`.
export interface DashboardAuthoring {
	listEventTypes(): Promise<EventTypeVocabulary>;
	previewMetric(metric: MetricDef): Promise<PreviewResult>;
	setDashboard(spec: DashboardDef): Promise<CommitResult>;
}

type AuditReader = Pick<
	Client<typeof AuditService>,
	"listAuditEventTypes" | "aggregateAuditLog"
>;

export interface DashboardAuthoringDeps {
	audit: AuditReader;
	// orgId scopes every audit read; the driver iterates within one org.
	orgId: string;
	// commit persists a validated spec and re-renders the canvas. In the app it
	// is the draft hook's setSpec; a harness passes its own capture.
	commit: (spec: DashboardDef) => void;
}

// The canonical validator checks a metric against the event/category namespace,
// not the richer registry projection; project the catalog down to the names it
// needs.
function toAuditVocabulary(vocab: EventTypeVocabulary): AuditVocabulary {
	return {
		eventTypes: vocab.events.map((e) => e.name),
		categories: vocab.categories,
	};
}

export function createDashboardAuthoring(
	deps: DashboardAuthoringDeps,
): DashboardAuthoring {
	const { audit, orgId, commit } = deps;

	// The audit event registry is server-owned and effectively static, so a
	// driver iterating on a spec should not re-fetch it on every preview/commit.
	// Cache the vocabulary for this authoring instance; a failed fetch clears the
	// cache so a transient error can be retried rather than pinned.
	let vocabPromise: Promise<EventTypeVocabulary> | undefined;
	function readVocabulary(): Promise<EventTypeVocabulary> {
		if (!vocabPromise) {
			vocabPromise = audit
				.listAuditEventTypes({})
				.then((res): EventTypeVocabulary => {
					const events: AuditEventTypeInfo[] = res.types.map((t) => ({
						name: t.name,
						version: t.version,
						category: t.category,
						owner: t.owner,
						deprecated: t.deprecated,
						description: t.description,
					}));
					return {
						events,
						categories: [...new Set(events.map((e) => e.category))],
					};
				})
				.catch((err) => {
					vocabPromise = undefined;
					throw err;
				});
		}
		return vocabPromise;
	}

	return {
		listEventTypes() {
			return readVocabulary();
		},

		async previewMetric(metric) {
			const vocab = await readVocabulary();
			const errors = validateMetric(metric, toAuditVocabulary(vocab));
			if (errors.length > 0) return { ok: false, errors };

			// The aggregate RPC is org-scoped: an empty orgId is not "this org" but
			// the platform-admin control-plane path (spans all tenants), which a
			// normal caller is denied. Mirror useMetric's "don't query until org is
			// resolved" guard and surface it as a precise, non-throwing result.
			if (orgId === "") {
				return {
					ok: false,
					errors: [
						{
							path: "orgId",
							code: "org_unresolved",
							message:
								"No organization is in scope yet; previews are unavailable until one resolves.",
						},
					],
				};
			}

			const { params, valueAlias } = compileMetricQuery(metric, orgId);
			const res = await audit.aggregateAuditLog(toAggregateRequest(params));
			return {
				ok: true,
				preview: shapeMetricSeries(toAggregateBuckets(res), metric, valueAlias),
			};
		},

		async setDashboard(spec) {
			const vocab = await readVocabulary();
			const errors = validateDashboard(spec, toAuditVocabulary(vocab));
			if (errors.length > 0) return { ok: false, errors };
			commit(spec);
			return { ok: true, spec };
		},
	};
}
