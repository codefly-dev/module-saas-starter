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

	it("rejects count_distinct until the audit RPC supports it", () => {
		const metric: SourceMetric = {
			id: "distinct_actors",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count_distinct",
		};

		expect(() => compileMetric(metric, resolve, { orgId: "o" })).toThrow(
			/count_distinct/,
		);
	});
});
