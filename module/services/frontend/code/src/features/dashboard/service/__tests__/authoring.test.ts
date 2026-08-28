import { describe, expect, it, vi } from "vitest";
import {
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	type MetricDef,
} from "../../model/schema";
import {
	createDashboardAuthoring,
	type DashboardAuthoringDeps,
} from "../authoring";

// A driver stand-in reads through the audit client; these fakes mimic the two
// RPCs the authoring surface touches. The registry names auth.login under
// "authentication"; the aggregate returns two ranked buckets.
function fakeAudit(): DashboardAuthoringDeps["audit"] {
	return {
		listAuditEventTypes: vi.fn(async () => ({
			$typeName: "saas.accounts.v1.ListAuditEventTypesResponse",
			types: [
				{
					$typeName: "saas.accounts.v1.AuditEventType",
					name: "auth.login",
					version: 1,
					category: "authentication",
					owner: "accounts",
					deprecated: false,
					description: "A user logged in.",
				},
				{
					$typeName: "saas.accounts.v1.AuditEventType",
					name: "org.created",
					version: 1,
					category: "organization",
					owner: "accounts",
					deprecated: false,
					description: "An organization was created.",
				},
			],
		})),
		aggregateAuditLog: vi.fn(async () => ({
			$typeName: "saas.accounts.v1.AggregateAuditLogResponse",
			buckets: [
				{
					$typeName: "saas.accounts.v1.AuditAggregateBucket",
					key: "org.created",
					count: BigInt(3),
					keys: ["org.created"],
					metrics: {},
				},
				{
					$typeName: "saas.accounts.v1.AuditAggregateBucket",
					key: "auth.login",
					count: BigInt(9),
					keys: ["auth.login"],
					metrics: {},
				},
			],
		})),
	} as unknown as DashboardAuthoringDeps["audit"];
}

function authoring(overrides: Partial<DashboardAuthoringDeps> = {}) {
	const commits: DashboardDef[] = [];
	const deps: DashboardAuthoringDeps = {
		audit: overrides.audit ?? fakeAudit(),
		orgId: overrides.orgId ?? "org-1",
		commit: overrides.commit ?? ((spec) => commits.push(spec)),
	};
	return { api: createDashboardAuthoring(deps), commits };
}

function spec(
	metrics: MetricDef[],
	extra: Partial<DashboardDef> = {},
): DashboardDef {
	return { version: DASHBOARD_SPEC_VERSION, metrics, ...extra };
}

const loginMetric: MetricDef = {
	title: "Logins over time",
	event: { type: "auth.login" },
	groupBy: "time",
	bucket: "day",
	chart: "line",
};

describe("dashboard authoring API", () => {
	it("enumerates the audit vocabulary a driver can reference", async () => {
		const { api } = authoring();
		const vocab = await api.listEventTypes();
		expect(vocab.events.map((e) => e.name)).toEqual([
			"auth.login",
			"org.created",
		]);
		expect(vocab.categories).toEqual(["authentication", "organization"]);
	});

	it("previews a metric as the same ranked series a card would render", async () => {
		const { api } = authoring();
		const result = await api.previewMetric({
			title: "Top events",
			groupBy: "event_type",
			chart: "bar",
			limit: 1,
		});
		expect(result.ok).toBe(true);
		if (!result.ok) return;
		// Ranked by value, capped at the top-N limit.
		expect(result.preview.points).toEqual([{ key: "auth.login", value: 9 }]);
		expect(result.preview.total).toBe(9);
	});

	it("previews a widened metric through the same compiled query a card uses", async () => {
		const audit = fakeAudit();
		let request: { metrics?: Array<{ op: string; alias: string }> } | undefined;
		(audit.aggregateAuditLog as ReturnType<typeof vi.fn>).mockImplementation(
			async (req: typeof request) => {
				request = req;
				return {
					$typeName: "saas.accounts.v1.AggregateAuditLogResponse",
					buckets: [
						{
							$typeName: "saas.accounts.v1.AuditAggregateBucket",
							key: "2026-08-01",
							count: BigInt(120),
							keys: ["2026-08-01"],
							metrics: { value: 4200 },
						},
						// A day with events but an undefined percentile — omitted alias.
						{
							$typeName: "saas.accounts.v1.AuditAggregateBucket",
							key: "2026-08-02",
							count: BigInt(7),
							keys: ["2026-08-02"],
							metrics: {},
						},
					],
				};
			},
		);
		const { api } = authoring({ audit });

		const result = await api.previewMetric({
			title: "p95 latency",
			event: { type: "auth.login" },
			groupBy: "time",
			bucket: "day",
			chart: "line",
			value: {
				op: "percentile",
				field: "payload:duration_ms",
				percentile: 0.95,
			},
		});

		expect(result.ok).toBe(true);
		if (!result.ok) return;
		// The request carried the percentile aggregation (not a bare count), and
		// the preview reads the aliased value while dropping the no-data day.
		expect(request?.metrics?.[0]).toMatchObject({
			op: "percentile",
			alias: "value",
		});
		expect(result.preview.points).toEqual([{ key: "2026-08-01", value: 4200 }]);
		expect(result.preview.total).toBe(4200);
	});

	it("rejects a preview that references an unknown event before querying", async () => {
		const audit = fakeAudit();
		const { api } = authoring({ audit });
		const result = await api.previewMetric({
			title: "Bad",
			event: { type: "auth.nope" },
			groupBy: "time",
			bucket: "day",
			chart: "line",
		});
		expect(result.ok).toBe(false);
		if (result.ok) return;
		expect(result.errors).toEqual([
			{
				path: "metric.event.type",
				code: "unknown_event_type",
				message: '"auth.nope" is not a registered audit event type.',
			},
		]);
		expect(audit.aggregateAuditLog).not.toHaveBeenCalled();
	});

	it("returns a structured error (not a throw) for a preview of a malformed metric", async () => {
		const audit = fakeAudit();
		const { api } = authoring({ audit });
		const result = await api.previewMetric(null as unknown as MetricDef);
		expect(result.ok).toBe(false);
		if (result.ok) return;
		expect(result.errors[0].code).toBe("invalid_spec");
		expect(audit.aggregateAuditLog).not.toHaveBeenCalled();
	});

	it("refuses to preview against an unresolved org instead of querying cross-tenant", async () => {
		const audit = fakeAudit();
		const { api } = authoring({ audit, orgId: "" });
		const result = await api.previewMetric({
			title: "Top events",
			groupBy: "event_type",
			chart: "bar",
		});
		expect(result.ok).toBe(false);
		if (result.ok) return;
		expect(result.errors).toEqual([
			{
				path: "orgId",
				code: "org_unresolved",
				message:
					"No organization is in scope yet; previews are unavailable until one resolves.",
			},
		]);
		expect(audit.aggregateAuditLog).not.toHaveBeenCalled();
	});

	it("fetches the audit vocabulary once per instance across calls", async () => {
		const audit = fakeAudit();
		const { api } = authoring({ audit });
		await api.listEventTypes();
		await api.previewMetric({
			title: "Top events",
			groupBy: "event_type",
			chart: "bar",
		});
		await api.setDashboard(
			spec([{ title: "Top events", groupBy: "event_type", chart: "bar" }]),
		);
		expect(audit.listAuditEventTypes).toHaveBeenCalledTimes(1);
	});

	it("commits a valid spec through the injected commit", async () => {
		const { api, commits } = authoring();
		const s = spec([loginMetric], { title: "Activity" });
		const result = await api.setDashboard(s);
		expect(result.ok).toBe(true);
		expect(commits).toEqual([s]);
	});

	it("accepts a partial spec (optional title/description omitted)", async () => {
		const { api, commits } = authoring();
		const s = spec([
			{ title: "Top events", groupBy: "event_type", chart: "bar" },
		]);
		const result = await api.setDashboard(s);
		expect(result.ok).toBe(true);
		expect(commits).toEqual([s]);
	});

	it("rejects a spec that fails shape validation and does not commit", async () => {
		const { api, commits } = authoring();
		const result = await api.setDashboard(
			spec([{ title: "", groupBy: "time", bucket: "day", chart: "line" }]),
		);
		expect(result.ok).toBe(false);
		if (result.ok) return;
		expect(result.errors[0].code).toBe("invalid_spec");
		expect(result.errors[0].message).toMatch(/title/);
		expect(commits).toEqual([]);
	});

	it("rejects a null metric entry as a structured error instead of throwing", async () => {
		const { api, commits } = authoring();
		const result = await api.setDashboard(spec([null as unknown as MetricDef]));
		expect(result.ok).toBe(false);
		if (result.ok) return;
		expect(result.errors[0].code).toBe("invalid_spec");
		expect(commits).toEqual([]);
	});

	it("rejects a spec that references an unknown event and does not commit", async () => {
		const { api, commits } = authoring();
		const result = await api.setDashboard(
			spec([
				{
					title: "Bad",
					event: { type: "auth.nope" },
					groupBy: "time",
					bucket: "day",
					chart: "line",
				},
			]),
		);
		expect(result.ok).toBe(false);
		if (result.ok) return;
		expect(result.errors).toEqual([
			{
				path: "metrics[0].event.type",
				code: "unknown_event_type",
				message: '"auth.nope" is not a registered audit event type.',
			},
		]);
		expect(commits).toEqual([]);
	});
});
