import type { Client } from "@connectrpc/connect";
import type { AuditEventTypeInfo } from "@/features/audit";
import {
	toAggregateBuckets,
	toAggregateRequest,
} from "@/features/audit/service/queries";
import type { AuditService } from "@/gen/saas/accounts/v1/audit_pb";
import {
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	type MetricDef,
} from "../model/schema";
import { assertDashboardSpec, DashboardSpecError } from "../model/validate";
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

// FieldError is one guard-rail failure returned to a driver: `code` is a stable
// token to branch on, `message` explains the failure, and `path` points at the
// offending spec location when the failure is field-specific (a vocabulary
// miss); a whole-spec shape violation carries the reason in `message` alone.
export interface FieldError {
	path?: string;
	code: string;
	message: string;
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

// The shape/coherence validator (assertDashboardSpec) throws on the first
// violation; the authoring surface reports failures as data, so a shape error
// is caught and rendered as a single structured FieldError.
function shapeErrors(run: () => void): FieldError[] {
	try {
		run();
		return [];
	} catch (err) {
		if (err instanceof DashboardSpecError) {
			return [{ code: "invalid_spec", message: err.message }];
		}
		throw err;
	}
}

// assertDashboardSpec covers shape and dimensional coherence but not whether a
// referenced event/category actually exists — that needs the live registry,
// which only this audit-aware layer has. checkMetricVocabulary closes that gap.
function checkMetricVocabulary(
	metric: MetricDef,
	vocab: EventTypeVocabulary,
	path: string,
): FieldError[] {
	const eventTypes = vocab.events.map((e) => e.name);
	const errors: FieldError[] = [];
	if (metric.event && !eventTypes.includes(metric.event.type)) {
		errors.push({
			path: `${path}.event.type`,
			code: "unknown_event_type",
			message: `"${metric.event.type}" is not a registered audit event type.`,
		});
	}
	if (
		metric.category !== undefined &&
		!vocab.categories.includes(metric.category)
	) {
		errors.push({
			path: `${path}.category`,
			code: "unknown_category",
			message: `"${metric.category}" is not a registered audit category.`,
		});
	}
	return errors;
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
			// Validate the metric's shape via the canonical spec validator by
			// wrapping it in a minimal spec, then check it against the vocabulary.
			const errors = shapeErrors(() =>
				assertDashboardSpec({
					version: DASHBOARD_SPEC_VERSION,
					metrics: [metric],
				}),
			);
			if (errors.length === 0) {
				errors.push(...checkMetricVocabulary(metric, vocab, "metric"));
			}
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
			const errors = shapeErrors(() => assertDashboardSpec(spec));
			if (errors.length === 0) {
				errors.push(
					...spec.metrics.flatMap((metric, i) =>
						checkMetricVocabulary(metric, vocab, `metrics[${i}]`),
					),
				);
			}
			if (errors.length > 0) return { ok: false, errors };
			commit(spec);
			return { ok: true, spec };
		},
	};
}
