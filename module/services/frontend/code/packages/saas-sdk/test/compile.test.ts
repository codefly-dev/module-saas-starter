import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { compileMetric } from "../src/datagraph/compile.js";
import type { SourceMetric } from "../src/schema.js";

const resolve = (name: string): string => `${name}.v1`;

describe("compileMetric", () => {
	it("binds a metric's filter and dimension to the org and resolves the event", () => {
		const metric: SourceMetric = {
			id: "logins_by_actor",
			kind: "source",
			filter: { event: "signed_in", actor: "actor_9" },
			groupBy: "actor",
			aggregation: "count",
		};

		const query = compileMetric(metric, resolve, { orgId: "org_1" });

		expect(query).toMatchObject({
			orgId: "org_1",
			eventType: "signed_in.v1",
			category: "",
			actorId: "actor_9",
			resource: "",
			groupBy: "actor",
			bucket: "",
		});
		expect(query.from).toBeUndefined();
		expect(query.to).toBeUndefined();
	});

	it("defaults the time bucket only for the time dimension", () => {
		const base: Omit<SourceMetric, "groupBy" | "bucket"> = {
			id: "logins",
			kind: "source",
			filter: { event: "signed_in" },
			aggregation: "count",
		};

		expect(
			compileMetric({ ...base, groupBy: "time" }, resolve, { orgId: "o" })
				.bucket,
		).toBe("day");
		expect(
			compileMetric({ ...base, groupBy: "time", bucket: "week" }, resolve, {
				orgId: "o",
			}).bucket,
		).toBe("week");
		expect(
			compileMetric({ ...base, groupBy: "event_type" }, resolve, { orgId: "o" })
				.bucket,
		).toBe("");
	});

	it("passes the context time window through as timestamps", () => {
		const from = new Date("2026-01-01T00:00:00Z");
		const to = new Date("2026-02-01T00:00:00Z");
		const metric: SourceMetric = {
			id: "windowed",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		};

		const query = compileMetric(metric, resolve, { orgId: "org_1", from, to });

		expect(query.from).toEqual(timestampFromDate(from));
		expect(query.to).toEqual(timestampFromDate(to));
	});

	it("leaves a plain count as a metrics-free COUNT(*) query", () => {
		const metric: SourceMetric = {
			id: "logins",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		};

		expect(compileMetric(metric, resolve, { orgId: "o" }).metrics).toEqual([]);
	});

	it("compiles count_distinct over a column into an aliased metric", () => {
		const metric: SourceMetric = {
			id: "distinct_actors",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count_distinct",
			field: "actor_id",
		};

		expect(compileMetric(metric, resolve, { orgId: "o" }).metrics).toEqual([
			{
				op: "count_distinct",
				field: "actor_id",
				percentile: 0,
				alias: "value",
			},
		]);
	});

	it("compiles a percentile over a payload field", () => {
		const metric: SourceMetric = {
			id: "p95_latency",
			kind: "source",
			filter: { event: "request_served" },
			groupBy: "time",
			bucket: "day",
			aggregation: "percentile",
			field: "payload:duration_ms",
			percentile: 0.95,
		};

		const query = compileMetric(metric, resolve, { orgId: "o" });

		expect(query.groupBy).toBe("time");
		expect(query.metrics).toEqual([
			{
				op: "percentile",
				field: "payload:duration_ms",
				percentile: 0.95,
				alias: "value",
			},
		]);
	});

	it("refuses to compile an org-unscoped query, failing closed", () => {
		const metric: SourceMetric = {
			id: "logins",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		};

		// A blank org would compile to an org-wide audit read: fail closed so a
		// caller that never bound a viewer org can't widen the read. A missing org
		// fails closed with the same message, not a raw TypeError.
		expect(() => compileMetric(metric, resolve, { orgId: "" })).toThrow(
			/without a viewer org/,
		);
		expect(() => compileMetric(metric, resolve, { orgId: "   " })).toThrow(
			/without a viewer org/,
		);
		expect(() =>
			compileMetric(metric, resolve, {
				orgId: undefined as unknown as string,
			}),
		).toThrow(/without a viewer org/);
	});

	it("trims the bound org so a padded context org still hits the exact-id lookup", () => {
		const metric: SourceMetric = {
			id: "logins",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		};

		expect(compileMetric(metric, resolve, { orgId: "  org_1  " }).orgId).toBe(
			"org_1",
		);
	});

	it("ignores an org injected onto the spec, binding only the context org", () => {
		// A spec parsed from untrusted JSON could carry an extra orgId; the compiler
		// binds the org from context alone, so an attempt to escape the viewer's org
		// through the spec is inert.
		const metric = {
			id: "logins",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
			orgId: "victim_org",
		} as unknown as SourceMetric;

		expect(compileMetric(metric, resolve, { orgId: "viewer_org" }).orgId).toBe(
			"viewer_org",
		);
	});

	it("passes a payload group dimension straight through", () => {
		const metric: SourceMetric = {
			id: "by_plan",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "payload:plan",
			aggregation: "count",
		};

		expect(compileMetric(metric, resolve, { orgId: "o" }).groupBy).toBe(
			"payload:plan",
		);
	});
});
