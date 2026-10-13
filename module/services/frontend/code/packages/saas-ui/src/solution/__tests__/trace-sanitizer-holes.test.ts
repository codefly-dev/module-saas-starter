import { describe, expect, it } from "vitest";

import {
	SanitizingSpanExporter,
	TRACE_ATTRIBUTE_VALUE_MAX_LENGTH,
	TRACE_SPAN_NAME_PLACEHOLDER,
	type TraceSanitizerPolicy,
} from "../trace-sanitizer.js";

/**
 * Three holes the sanitizer had when it landed, each with the input that got
 * through. The sanitizer's whole job is that an attribute nobody anticipated
 * is dropped rather than exported, so each of these is a test for the one
 * thing the module exists to do.
 */

// Only the fields sanitize() reads. A real ReadableSpan carries more, and
// nothing here depends on the rest.
function span(over: Record<string, unknown> = {}): never {
	const spanContext = {
		traceId: "a".repeat(32),
		spanId: "b".repeat(16),
		traceFlags: 1,
	};
	return {
		name: "GET",
		kind: 2,
		spanContext: () => spanContext,
		parentSpanContext: undefined,
		startTime: [0, 0],
		endTime: [0, 1],
		duration: [0, 1],
		ended: true,
		status: { code: 0, message: "" },
		attributes: {},
		links: [],
		events: [],
		resource: { attributes: {} },
		instrumentationScope: { name: "probe", version: "1" },
		droppedAttributesCount: 0,
		droppedEventsCount: 0,
		droppedLinksCount: 0,
		...over,
	} as never;
}

function exporter(policy: TraceSanitizerPolicy): SanitizingSpanExporter {
	return new SanitizingSpanExporter(
		{ export: () => {}, shutdown: async () => {} } as never,
		policy,
	);
}

describe("an allowed array attribute cannot smuggle a payload", () => {
	// An object stringifies to "[object Object]" — 15 characters — so measuring
	// String(item) put any object under the 256-character cap. This exported
	// [{"card":"4111111111111111","cvv":"123"}] verbatim.
	it("drops an array holding objects, whatever they stringify to", () => {
		const out = exporter({ allowedAttributeKeys: ["db.params"] }).sanitize(
			span({
				attributes: {
					"db.params": [{ card: "4111111111111111", cvv: "123" }] as never,
				},
			}),
		);

		expect(out.attributes["db.params"]).toBeUndefined();
	});

	it("drops an array holding a nested array", () => {
		const out = exporter({ allowedAttributeKeys: ["k"] }).sanitize(
			span({ attributes: { k: [["a"], ["b"]] as never } }),
		);

		expect(out.attributes.k).toBeUndefined();
	});

	it("drops an element longer than the cap even when the total fits", () => {
		const out = exporter({ allowedAttributeKeys: ["k"] }).sanitize(
			span({
				attributes: {
					k: [`${"x".repeat(TRACE_ATTRIBUTE_VALUE_MAX_LENGTH + 1)}`],
				},
			}),
		);

		expect(out.attributes.k).toBeUndefined();
	});

	it("keeps an ordinary primitive array, and nulls within it", () => {
		const out = exporter({ allowedAttributeKeys: ["k", "n"] }).sanitize(
			span({ attributes: { k: ["a", "b"], n: [1, null, 3] as never } }),
		);

		expect(out.attributes.k).toEqual(["a", "b"]);
		expect(out.attributes.n).toEqual([1, null, 3]);
	});

	// Spans export on a batch delay, so the array the span still holds must not
	// be the array that was vetted.
	it("hands back a copy, so a later mutation cannot change what exports", () => {
		const live = ["a"];
		const out = exporter({ allowedAttributeKeys: ["k"] }).sanitize(
			span({ attributes: { k: live } }),
		);
		live.push("added-after-sanitize");

		expect(out.attributes.k).toEqual(["a"]);
	});
});

describe("a span name is vetted by default, not merely query-stripped", () => {
	// Omitting allowedSpanNames used to mean "anything under the cap with no
	// query", so this name exported verbatim. Every test in the suite that came
	// with the feature passed an explicit allowlist, so nothing covered it.
	it("placeholders a free-form name when no allowlist is given", () => {
		const leaky =
			"load user alice@example.com reset token tok_live_51H8sQ2abcdef";
		const out = exporter({ allowedAttributeKeys: [] }).sanitize(
			span({ name: leaky }),
		);

		expect(out.name).toBe(TRACE_SPAN_NAME_PLACEHOLDER);
	});

	it("placeholders an event name the same way", () => {
		const out = exporter({ allowedAttributeKeys: [] }).sanitize(
			span({
				events: [
					{
						time: [0, 0],
						name: "sent alice@example.com her statement",
						attributes: {},
						droppedAttributesCount: 0,
					},
				],
			}),
		);

		expect(out.events[0]?.name).toBe(TRACE_SPAN_NAME_PLACEHOLDER);
	});

	it("still exports a bare HTTP method, which carries nothing", () => {
		for (const name of ["GET", "POST", "HTTP DELETE"]) {
			expect(
				exporter({ allowedAttributeKeys: [] }).sanitize(span({ name })).name,
			).toBe(name);
		}
	});

	it("exports a name the caller vouched for", () => {
		const out = exporter({
			allowedAttributeKeys: [],
			allowedSpanNames: ["page.load"],
		}).sanitize(span({ name: "page.load" }));

		expect(out.name).toBe("page.load");
	});
});

describe("a consumer can withhold a standard transport key", () => {
	// url.path exports query-stripped, but a path SEGMENT is the caller's to
	// shape: /reset/<token>/confirm survives the query cut intact. A consumer
	// whose paths carry identifiers needs a way to drop it without losing the
	// method and server attributes beside it.
	it("drops url.path when the policy denies it, keeping its neighbours", () => {
		const attributes = {
			"url.path": "/reset/tok_live_51H8sQ2abcdef/confirm",
			"http.request.method": "GET",
			"server.address": "app.example",
		};
		const out = exporter({
			allowedAttributeKeys: [],
			deniedAttributeKeys: ["url.path"],
		}).sanitize(span({ attributes }));

		expect(out.attributes["url.path"]).toBeUndefined();
		expect(out.attributes["http.request.method"]).toBe("GET");
		expect(out.attributes["server.address"]).toBe("app.example");
	});

	it("exports url.path query-stripped when the policy does not deny it", () => {
		const out = exporter({ allowedAttributeKeys: [] }).sanitize(
			span({ attributes: { "url.path": "/items?token=secret#frag" } }),
		);

		expect(out.attributes["url.path"]).toBe("/items");
	});

	it("cannot be used to re-admit a denied key", () => {
		const out = exporter({
			allowedAttributeKeys: ["url.full"],
			deniedAttributeKeys: [],
		}).sanitize(
			span({ attributes: { "url.full": "https://app.example/x?token=s" } }),
		);

		expect(out.attributes["url.full"]).toBeUndefined();
	});
});
