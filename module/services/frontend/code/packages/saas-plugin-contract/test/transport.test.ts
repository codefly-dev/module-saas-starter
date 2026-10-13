import { describe, expect, it } from "vitest";

import {
	connectProcedureTargetPath,
	encodeFrontendServicePath,
	frontendServiceTargetPath,
	isConnectProcedurePath,
	isConnectServiceName,
	isFrontendServicePath,
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
