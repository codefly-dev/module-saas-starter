import { describe, expect, it } from "vitest";

import {
	forwardTraceContext,
	parseTraceparent,
	parseTracestate,
	serializeTraceparent,
} from "@/lib/trace-context";

const TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";
const PARENT_ID = "00f067aa0ba902b7";
const TRUSTED = { honorSampledFlag: true } as const;
const UNTRUSTED = { honorSampledFlag: false } as const;

/** The round trip both boundaries perform: parse, then serialize under a policy. */
function relayed(
	raw: string,
	policy: { honorSampledFlag: boolean },
): string | null {
	const parsed = parseTraceparent(raw);
	return parsed === null ? null : serializeTraceparent(parsed, policy);
}

describe("traceparent", () => {
	it("relays a version-00 value unchanged, every flag bit included", () => {
		for (const flags of ["00", "01", "03", "ff"]) {
			const value = `00-${TRACE_ID}-${PARENT_ID}-${flags}`;
			expect(relayed(value, TRUSTED)).toBe(value);
		}
	});

	// The defect this replaces rewrote any later version as `00-…-<sampled>`,
	// which made the host assert a context nobody produced and destroyed the
	// W3C Level 2 `random` bit (0x02) that consistent-probability samplers read.
	it.each([
		["extra fields", `cc-${TRACE_ID}-${PARENT_ID}-03-future-field`],
		["no extra fields", `cc-${TRACE_ID}-${PARENT_ID}-03`],
		["a trailing separator", `01-${TRACE_ID}-${PARENT_ID}-03-`],
		["every flag bit set", `cc-${TRACE_ID}-${PARENT_ID}-ff-x`],
		["no sampled bit", `cc-${TRACE_ID}-${PARENT_ID}-fe`],
	])("relays a later version with %s unchanged", (_, value) => {
		expect(relayed(value, TRUSTED)).toBe(value);
	});

	it.each([
		["malformed", "not-a-traceparent"],
		["trailing dash", `00-${TRACE_ID}-${PARENT_ID}-01-`],
		["trailing field on version 00", `00-${TRACE_ID}-${PARENT_ID}-01-extra`],
		["uppercase hex", `00-${TRACE_ID.toUpperCase()}-${PARENT_ID}-01`],
		["zero trace id", `00-${"0".repeat(32)}-${PARENT_ID}-01`],
		["zero parent id", `00-${TRACE_ID}-${"0".repeat(16)}-01`],
		["invalid version ff", `ff-${TRACE_ID}-${PARENT_ID}-01`],
		["non-hex flags", `00-${TRACE_ID}-${PARENT_ID}-0g`],
		["short trace id", `00-${TRACE_ID.slice(1)}-${PARENT_ID}-01`],
		[
			"two values",
			`00-${TRACE_ID}-${PARENT_ID}-01, 00-${TRACE_ID}-${PARENT_ID}-00`,
		],
		[
			"two values of a later version",
			`cc-${TRACE_ID}-${PARENT_ID}-01, cc-${TRACE_ID}-${PARENT_ID}-00`,
		],
		[
			"a later version with an unsafe character",
			`cc-${TRACE_ID}-${PARENT_ID}-01-a\x7fb`,
		],
		["a later version without a separator", `cc-${TRACE_ID}-${PARENT_ID}-01x`],
	])("does not parse a %s value", (_, value) => {
		expect(parseTraceparent(value)).toBeNull();
	});

	// The sampling decision is the one field a policy may change, because it is
	// an instruction to the upstream's sampler rather than inert metadata.
	it.each([
		["01", "00"],
		["03", "02"],
		["ff", "fe"],
		["00", "00"],
		["fe", "fe"],
	])(
		"clears only the sampled bit of %s for an untrusted caller, leaving %s",
		(flags, expected) => {
			expect(relayed(`00-${TRACE_ID}-${PARENT_ID}-${flags}`, UNTRUSTED)).toBe(
				`00-${TRACE_ID}-${PARENT_ID}-${expected}`,
			);
		},
	);

	it("clears the sampled bit in place, keeping a later version and its tail", () => {
		expect(
			relayed(`cc-${TRACE_ID}-${PARENT_ID}-03-future-field`, UNTRUSTED),
		).toBe(`cc-${TRACE_ID}-${PARENT_ID}-02-future-field`);
	});
});

describe("tracestate", () => {
	it("re-serializes a valid list, trimming the optional whitespace", () => {
		expect(parseTracestate(" vendor=abc ,, other@sys=x y\t")).toBe(
			"vendor=abc,other@sys=x y",
		);
	});

	// Rule 1: an empty list member is skipped, not a syntax error.
	it("skips empty list members", () => {
		expect(parseTracestate("a=1,,,b=2")).toBe("a=1,b=2");
		expect(parseTracestate(",")).toBeNull();
	});

	// Rule 2: a member that does not parse discards the whole list.
	it.each([
		["a duplicate key", "a=1,b=2,a=3"],
		["an invalid key", "A=1"],
		["a key starting with a digit before @", "1a=1"],
		["an empty value", "a=1,b="],
		["a non-ASCII value", "a=café"],
		["a value containing =", "a=b=c"],
		["a member without =", "a=1,b"],
		["whitespace around =", "a = 1"],
	])("discards the whole list for %s", (_, value) => {
		expect(parseTracestate(value)).toBeNull();
	});

	// Rule 3: both bounds truncate from the right. Discarding for the member
	// bound while truncating for the byte bound dropped every other vendor's
	// state over a single over-long hop.
	it("truncates past 32 members instead of discarding the list", () => {
		const members = Array.from({ length: 33 }, (_, i) => `k${i}=v`);
		expect(parseTracestate(members.join(","))).toBe(
			members.slice(0, 32).join(","),
		);
	});

	it("keeps exactly 32 members", () => {
		const members = Array.from({ length: 32 }, (_, i) => `k${i}=v`);
		expect(parseTracestate(members.join(","))).toBe(members.join(","));
	});

	it("drops members with an over-long value before trimming the right", () => {
		const large = `big=${"x".repeat(200)}`;
		const small = Array.from(
			{ length: 30 },
			(_, i) => `k${String(i).padStart(2, "0")}=${"v".repeat(9)}`,
		);
		// 30 small members of 13 bytes = 419 bytes with separators; the large
		// member pushes the list past 512 and is the one dropped.
		expect(
			parseTracestate([small[0], large, ...small.slice(1)].join(",")),
		).toBe(small.join(","));
	});

	// The threshold is on the VALUE, not on `key=value`: measuring the member
	// evicted a 127-byte value whose key pushed the member over 128, which is
	// not the fat member the rule means to drop.
	it("measures the value, not key=value, against the 128-byte threshold", () => {
		const shortValue = `k=${"v".repeat(127)}`;
		const fat = Array.from({ length: 4 }, (_, i) => `b${i}=${"x".repeat(120)}`);
		expect(shortValue.length).toBeGreaterThan(128);
		// Nothing exceeds a 128-byte VALUE, so the over-long pass finds no
		// candidate and the list is trimmed from the right: 129 + 3 x 123 + 3
		// separators = 501 bytes, so four members survive and the short-valued
		// one is kept. Measuring `key=value` instead made it the first eviction
		// and left all four fat members.
		expect(parseTracestate([shortValue, ...fat].join(","))).toBe(
			[shortValue, ...fat.slice(0, 3)].join(","),
		);
	});

	it("trims from the right when no member has an over-long value", () => {
		const members = Array.from(
			{ length: 5 },
			(_, i) => `k${i}=${"v".repeat(120)}`,
		);
		// Each member is 123 bytes: four fit in 495 bytes, five would be 619.
		expect(parseTracestate(members.join(","))).toBe(
			members.slice(0, 4).join(","),
		);
	});

	it("keeps every forwarded list within 512 bytes", () => {
		const members = Array.from(
			{ length: 32 },
			(_, i) => `k${i}=${"v".repeat(60)}`,
		);
		const forwarded = parseTracestate(members.join(",")) ?? "";
		expect(new TextEncoder().encode(forwarded).length).toBeLessThanOrEqual(512);
	});
});

describe("forwardTraceContext", () => {
	function forwarded(
		headers: Record<string, string>,
		policy: { honorSampledFlag: boolean },
	): Headers {
		const to = new Headers();
		forwardTraceContext(new Headers(headers), to, policy);
		return to;
	}

	it("carries traceparent and tracestate for a trusted caller", () => {
		const to = forwarded(
			{
				traceparent: `00-${TRACE_ID}-${PARENT_ID}-01`,
				tracestate: "vendor=abc",
			},
			TRUSTED,
		);
		expect(to.get("traceparent")).toBe(`00-${TRACE_ID}-${PARENT_ID}-01`);
		expect(to.get("tracestate")).toBe("vendor=abc");
	});

	// An untrusted caller's tracestate stays behind because vendors re-state the
	// sampling decision there (OpenTelemetry's `ot=` entry), so forwarding it
	// would hand back what clearing the sampled bit just took away.
	it("links the trace but withholds the decision from an untrusted caller", () => {
		const to = forwarded(
			{
				traceparent: `00-${TRACE_ID}-${PARENT_ID}-01`,
				tracestate: "ot=p:8;r:62,vendor=abc",
			},
			UNTRUSTED,
		);
		expect(to.get("traceparent")).toBe(`00-${TRACE_ID}-${PARENT_ID}-00`);
		expect(to.has("tracestate")).toBe(false);
	});

	it("drops tracestate without a traceparent", () => {
		const to = forwarded({ tracestate: "vendor=abc" }, TRUSTED);
		expect(to.has("traceparent")).toBe(false);
		expect(to.has("tracestate")).toBe(false);
	});

	it("leaves the upstream headers untouched when nothing parses", () => {
		const to = forwarded(
			{ traceparent: "nonsense", tracestate: "vendor=abc" },
			TRUSTED,
		);
		expect([...to.keys()]).toEqual([]);
	});

	it("merges repeated tracestate header values with a comma", () => {
		const from = new Headers({ traceparent: `00-${TRACE_ID}-${PARENT_ID}-01` });
		from.append("tracestate", "a=1");
		from.append("tracestate", "b=2,c=3");
		const to = new Headers();
		forwardTraceContext(from, to, TRUSTED);
		expect(to.get("tracestate")).toBe("a=1,b=2,c=3");
	});

	it("never forwards baggage or any other caller header", () => {
		const to = forwarded(
			{
				traceparent: `00-${TRACE_ID}-${PARENT_ID}-01`,
				baggage: "userId=alice",
				"x-b3-traceid": TRACE_ID,
			},
			TRUSTED,
		);
		expect([...to.keys()].sort()).toEqual(["traceparent"]);
	});
});
