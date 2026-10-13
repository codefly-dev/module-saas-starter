import { describe, expect, it } from "vitest";

import {
	connectProcedureTargetPath,
	encodeFrontendServicePath,
	frontendServiceTargetPath,
	isConnectProcedurePath,
	isConnectServiceName,
	isFrontendServicePath,
	isFrontendServiceRoutePrefix,
	isServiceableFrontendService,
	resolveConnectProcedure,
} from "../src/transport.js";

// The four custom-verb shapes a module's bindings actually produce, as the
// Google HTTP form writes them: a collection verb, a sub-collection verb, a verb
// on an item identified by a path parameter, and a verb on a named resource.
// A parameter is already substituted by the time a path reaches the transport,
// so these are spelled with a value where the binding has `{id}`.
const CUSTOM_VERB_ROUTES: ReadonlyArray<readonly string[]> = [
	["records:recover"],
	["records", "record-1", "commands:recover"],
	["records", "record-1", "interactions", "interaction-1:respond"],
	["triggers", "nightly:fire"],
];

describe("relative plugin service paths", () => {
	it.each(CUSTOM_VERB_ROUTES.map((segments) => [segments.join("/"), segments]))(
		"carries the custom-verb route %s",
		(_label, segments) => {
			expect(isFrontendServicePath(segments)).toBe(true);
		},
	);

	it("carries plain reads and the reserved capability probe", () => {
		expect(isFrontendServicePath(["traffic"])).toBe(true);
		expect(isFrontendServicePath(["records", "record-1", "structure"])).toBe(
			true,
		);
		expect(isFrontendServicePath([".well-known", "capabilities"])).toBe(true);
	});

	it.each([
		["an empty path", []],
		["an empty segment", ["records", ""]],
		["a dot segment", ["."]],
		["a traversal segment", ["records", "..", "secrets"]],
		["an encoded separator", ["a%2fb"]],
		["a literal separator", ["a/b"]],
		["a backslash", ["a\\b"]],
		["an oversized segment", ["a".repeat(257)]],
		// A colon is the custom-verb separator, so it is admitted exactly where
		// the form puts one and nowhere else. An interior colon would be a
		// resource whose name carries the separator, and two colons in one
		// segment is not a verb at all.
		["an interior colon", ["records:recover", "commands"]],
		["two colons", ["records:recover:again"]],
		["a colon with no resource", [":recover"]],
		["a colon with no verb", ["records:"]],
		["an upper-case verb", ["records:Recover"]],
		["a verb with a separator", ["records:recover-lost"]],
		// The resource half of a custom verb is a segment like any other, so the
		// not-dot rule reaches it too. `..:recover` is a traversal wearing a
		// verb: admitted, it would bind `..` as the resource upstream.
		["a traversal with a verb", ["..:recover"]],
		["a traversal with a verb mid-path", ["records", "..:recover"]],
		["a dot with a verb", [".:recover"]],
		["an encoded separator in the resource half", ["a%2fb:recover"]],
		["a literal separator in the resource half", ["a/b:recover"]],
	])("refuses %s", (_label, segments) => {
		expect(isFrontendServicePath(segments)).toBe(false);
	});
});

describe("connect procedure paths", () => {
	it("names one procedure of a fully qualified service", () => {
		expect(
			isConnectProcedurePath([
				"example.records.v1.RecordService",
				"RecoverRecord",
			]),
		).toBe(true);
		expect(
			isConnectProcedurePath([
				"saas.frontend.plugin.v1.FrontendPluginCapabilityService",
				"GetFrontendPluginCapabilities",
			]),
		).toBe(true);
	});

	it.each([
		["a REST-shaped path", ["records", "record-1", "structure"]],
		["an unqualified service", ["RecordService", "RecoverRecord"]],
		["a missing method", ["example.records.v1.RecordService"]],
		[
			"a trailing segment",
			["example.records.v1.RecordService", "Recover", "x"],
		],
		["a custom verb", ["example.records.v1.RecordService", "records:recover"]],
		["a hyphenated service", ["example.records-v1.RecordService", "Recover"]],
	])("refuses %s", (_label, segments) => {
		expect(isConnectProcedurePath(segments)).toBe(false);
	});
});

describe("the connect services a requirement declares", () => {
	// A Connect procedure's path carries its own service name, so the route
	// prefix cannot bound which service an alias speaks to — only the declared
	// list can. Before it, a requirement declaring the origin root reached every
	// service the upstream served, as the viewer.
	const SERVICES = [
		"example.records.v1.RecordService",
		"example.records.v1.CommandService",
	];

	it("admits a procedure of a declared service", () => {
		expect(
			resolveConnectProcedure(SERVICES, [SERVICES[0], "RecoverRecord"]),
		).toEqual({
			service: "example.records.v1.RecordService",
			method: "RecoverRecord",
		});
		expect(
			resolveConnectProcedure(SERVICES, [SERVICES[1], "Dispatch"]),
		).toEqual({
			service: "example.records.v1.CommandService",
			method: "Dispatch",
		});
	});

	it.each([
		[
			"a service the requirement never declared",
			SERVICES,
			["example.secrets.v1.SecretService", "Read"],
		],
		[
			"a service that only looks declared",
			SERVICES,
			["example.records.v1.RecordServiceX", "Recover"],
		],
		[
			"an empty declaration",
			[],
			["example.records.v1.RecordService", "Recover"],
		],
		[
			"a declaration of an unqualified name",
			["RecordService"],
			["RecordService", "Recover"],
		],
		["a declaration carrying a separator", ["a/b"], ["a/b", "Recover"]],
		["a REST-shaped path", SERVICES, ["records", "record-1"]],
		["a method-only path", SERVICES, ["RecoverRecord"]],
		["a trailing segment", SERVICES, [SERVICES[0], "Recover", "x"]],
	])("refuses %s", (_label, services, segments) => {
		expect(resolveConnectProcedure(services, segments)).toBeNull();
	});

	it("names a fully qualified service and nothing else", () => {
		expect(isConnectServiceName("example.records.v1.RecordService")).toBe(true);
		expect(isConnectServiceName("RecordService")).toBe(false);
		expect(isConnectServiceName("example.records-v1.RecordService")).toBe(
			false,
		);
		expect(
			isConnectServiceName("example.records.v1.RecordService/Recover"),
		).toBe(false);
		expect(isConnectServiceName("")).toBe(false);
	});
});

describe("the path a declared binding lands on", () => {
	it("leaves a custom verb's colon literal", () => {
		expect(encodeFrontendServicePath(["records:recover"])).toBe(
			"records:recover",
		);
		expect(
			encodeFrontendServicePath([
				"records",
				"record-1",
				"interactions",
				"interaction-1:respond",
			]),
		).toBe("records/record-1/interactions/interaction-1:respond");
	});

	it("appends to the requirement's own prefix", () => {
		expect(
			frontendServiceTargetPath("/v1/example", ["records", "record-1:recover"]),
		).toBe("/v1/example/records/record-1:recover");
		expect(frontendServiceTargetPath("/v1/example", [])).toBe("/v1/example");
	});

	it("puts an admitted procedure under the requirement's mount prefix", () => {
		const procedure = {
			service: "example.records.v1.RecordService",
			method: "RecoverRecord",
		};
		// The root prefix is the Connect protocol's own path from the origin;
		// a mount prefix puts the same procedure under it. Both are built from
		// the procedure that was ADMITTED, never from the segments as they
		// arrived, so the bound and the destination cannot disagree.
		expect(connectProcedureTargetPath("/", procedure)).toBe(
			"/example.records.v1.RecordService/RecoverRecord",
		);
		expect(connectProcedureTargetPath("/v1/connect", procedure)).toBe(
			"/v1/connect/example.records.v1.RecordService/RecoverRecord",
		);
	});

	it("puts a root-prefixed requirement's path at the origin root", () => {
		expect(
			frontendServiceTargetPath("/", [
				"example.records.v1.RecordService",
				"RecoverRecord",
			]),
		).toBe("/example.records.v1.RecordService/RecoverRecord");
		expect(frontendServiceTargetPath("/", [])).toBe("/");
	});
});

// THE WHOLE PREFIX RULE, IN ONE PLACE (issue #1121).
//
// The composition validated the full form at build time while the host's
// runtime re-check compared only against the root, so a prefix the composition
// would have refused passed the runtime one — and a REST entry declaring `""`
// landed every relative path on the upstream origin. Both sides read this now,
// so the table below is the rule both obey.
describe("isFrontendServiceRoutePrefix", () => {
	it.each([
		["an absolute path of safe segments", "rest", "/v1/example", true],
		["one segment", "rest", "/v1", true],
		// The root is Connect-only: it says "where the Connect protocol puts a
		// procedure", which is the origin. A REST alias declaring it has no
		// prefix at all.
		["the root on a connect requirement", "connect", "/", true],
		["the root on a rest requirement", "rest", "/", false],
		// The empty string is NOT the root, which is exactly why comparing
		// against the root alone admitted it.
		["the empty string", "rest", "", false],
		["the empty string on connect", "connect", "", false],
		["a relative prefix", "rest", "v1/example", false],
		["a traversal", "rest", "/v1/../private", false],
		["a dot segment", "rest", "/v1/./private", false],
		["an empty segment", "rest", "/v1//private", false],
		["a trailing slash", "rest", "/v1/example/", false],
		["an encoded separator", "rest", "/v1/%2fprivate", false],
		["a query", "rest", "/v1/example?all=1", false],
		["a fragment", "rest", "/v1/example#x", false],
	])("%s", (_name, protocol, prefix, admitted) => {
		expect(
			isFrontendServiceRoutePrefix(protocol as "connect" | "rest", prefix),
		).toBe(admitted);
	});

	it("refuses anything that is not a string", () => {
		for (const value of [undefined, null, 1, {}, ["/v1"]]) {
			expect(isFrontendServiceRoutePrefix("rest", value)).toBe(false);
			expect(isFrontendServiceRoutePrefix("connect", value)).toBe(false);
		}
	});
});

// What a host may proxy at all, independent of the path asked for.
describe("isServiceableFrontendService", () => {
	it("admits the two shapes a composition installs", () => {
		expect(
			isServiceableFrontendService({
				protocol: "rest",
				routePrefix: "/v1/example",
			}),
		).toBe(true);
		expect(
			isServiceableFrontendService({
				protocol: "connect",
				routePrefix: "/",
				connectServices: ["example.records.v1.RecordService"],
			}),
		).toBe(true);
		expect(
			isServiceableFrontendService({
				protocol: "connect",
				routePrefix: "/v1/connect",
				connectServices: ["example.records.v1.RecordService"],
			}),
		).toBe(true);
	});

	// The probe the review used: a REST entry whose prefix is the empty string
	// proxies its whole upstream, because every relative path lands at the
	// origin. `admin/secret` is a real path on a real backend.
	it("refuses a rest entry with no prefix", () => {
		expect(
			isServiceableFrontendService({ protocol: "rest", routePrefix: "" }),
		).toBe(false);
		expect(frontendServiceTargetPath("", ["admin", "secret"])).toBe(
			"/admin/secret",
		);
	});

	// A Connect entry whose service list is not an array has a `length` and no
	// `every`: a check that assumed an array threw out of the request handler
	// as a 500 rather than refusing the alias.
	it.each([
		["a string", "example.records.v1.RecordService"],
		["an object", { 0: "example.records.v1.RecordService", length: 1 }],
		["a number", 1],
		["null", null],
		["absent", undefined],
		["empty", []],
		["an unusable name", ["not a service name"]],
		["a non-string entry", [1]],
	])("refuses a connect entry whose services are %s", (_name, services) => {
		expect(
			isServiceableFrontendService({
				protocol: "connect",
				routePrefix: "/",
				connectServices: services as unknown as readonly string[],
			}),
		).toBe(false);
	});

	// On a REST alias the field is meaningless rather than disqualifying: the
	// composition refuses it, and a projection carrying one is still bounded by
	// its prefix.
	it("ignores connect services on a rest entry", () => {
		expect(
			isServiceableFrontendService({
				protocol: "rest",
				routePrefix: "/v1/example",
				connectServices: ["nonsense"] as unknown as readonly string[],
			}),
		).toBe(true);
	});
});
