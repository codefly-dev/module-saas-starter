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

// PreconditionCode enumerates the pending-channel tokens a driver branches on
// to tell one unmet precondition from another. Unlike a FieldError's open
// `code` — validation codes are many and a driver reads `path`/`message` rather
// than switching on them — a precondition is a control-flow signal a driver
// must dispatch on, so the closed union makes a `switch` exhaustive: adding a
// precondition here surfaces every unhandled branch at compile time.
export type PreconditionCode = "org_unresolved";

// A driver-facing operation returns either a value or, on failure, one of two
// distinct kinds — never a bare throw for a spec it can fix. A "validation"
// failure is the driver's to fix: the spec is malformed or references something
// unregistered, and `errors` points at each offending field. A "pending"
// failure is not fixable by editing the spec — a precondition (an organization
// in scope) isn't met yet, so the driver waits and retries. It carries a
// `code`/`message` (like a FieldError, minus `path`, since the block is not a
// spec field): `code` is the closed `PreconditionCode` a driver branches on to
// tell one precondition from another, `message` explains it in prose for a
// human. The `kind` discriminant lets a driver branch "fix your spec" vs "wait
// for context" once; matching a precondition's `code` never crosses into the
// validation channel.
export type PreviewResult =
	| { ok: true; preview: MetricPreview }
	| { ok: false; kind: "validation"; errors: FieldError[] }
	| { ok: false; kind: "pending"; code: PreconditionCode; message: string };

// CommitResult writes only the local draft, so it has no precondition to wait
// on — its sole failure kind is "validation". It still carries `kind` so the
// same code that renders a preview's validation errors also renders a commit's:
// both failures share the `{ kind: "validation"; errors: FieldError[] }` shape.
// (A `pending` check against a CommitResult is a compile error, not a runtime
// branch — the shared benefit is the rendering shape, not a uniform switch.)
export type CommitResult =
	| { ok: true; spec: DashboardDef }
	| { ok: false; kind: "validation"; errors: FieldError[] };

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
			if (errors.length > 0) return { ok: false, kind: "validation", errors };

			// The aggregate RPC is org-scoped: an empty orgId is not "this org" but
			// the platform-admin control-plane path (spans all tenants), which a
			// normal caller is denied. Mirror useMetric's "don't query until org is
			// resolved" guard and surface it as a precise, non-throwing result. This
			// is a precondition, not a spec defect, so it rides the "pending" channel.
			if (orgId === "") {
				return {
					ok: false,
					kind: "pending",
					code: "org_unresolved",
					message:
						"No organization is in scope yet; previews are unavailable until one resolves.",
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
			if (errors.length > 0) return { ok: false, kind: "validation", errors };
			commit(spec);
			return { ok: true, spec };
		},
	};
}
