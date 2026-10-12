import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import type { AddressInfo } from "node:net";
import { join } from "node:path";
import { SpanStatusCode, trace } from "@opentelemetry/api";
import { BasicTracerProvider } from "@opentelemetry/sdk-trace-web";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	TRACE_ATTRIBUTE_VALUE_MAX_LENGTH,
	TRACE_SPAN_NAME_PLACEHOLDER,
} from "../trace-sanitizer.js";
import {
	type BrowserTracingOptions,
	installBrowserTracing,
	OTEL_API_VERSION,
	resetBrowserTracingForTests,
	resolveTraceExportEndpoint,
	TRACE_EXPORT_META_NAME,
} from "../tracing.js";

const NO_KEYS = { allowedAttributeKeys: [] };
const OTEL_API_GLOBAL = Symbol.for("opentelemetry.js.api.1");

const CANARY = {
	query: "CANARY-QUERY-TOKEN",
	body: "CANARY-BODY-TEXT",
	userAgent: "CANARY-USER-AGENT",
	exception: "CANARY-EXCEPTION-MESSAGE",
	status: "CANARY-STATUS-DESCRIPTION",
	name: "CANARY-SPAN-NAME",
	peer: "CANARY-PEER-ADDRESS",
};

type Registry = Record<string | symbol, unknown>;

afterEach(() => {
	resetBrowserTracingForTests();
	for (const meta of document.querySelectorAll("meta")) meta.remove();
	vi.restoreAllMocks();
	vi.unstubAllEnvs();
});

/** Starts a local OTLP/HTTP collector and resolves with the first body it receives. */
function collector(): Promise<{ url: string; payload: Promise<string> }> {
	return new Promise((ready) => {
		let deliver: (body: string) => void = () => {};
		const payload = new Promise<string>((resolveBody) => {
			deliver = resolveBody;
		});
		const server = createServer((req, res) => {
			const chunks: Buffer[] = [];
			req.on("data", (chunk: Buffer) => chunks.push(chunk));
			req.on("end", () => {
				res.writeHead(200).end();
				server.close();
				deliver(Buffer.concat(chunks).toString("utf8"));
			});
		});
		server.listen(0, "127.0.0.1", () => {
			const { port } = server.address() as AddressInfo;
			ready({ url: `http://127.0.0.1:${port}/v1/traces`, payload });
		});
	});
}

type OtlpAttribute = { key: string; value: Record<string, unknown> };
type OtlpSpan = {
	name: string;
	attributes: OtlpAttribute[];
	status: { message?: string };
	events: { name: string; attributes: OtlpAttribute[] }[];
};

function attributeKeys(attributes: OtlpAttribute[]): string[] {
	return attributes.map((attribute) => attribute.key).sort();
}

describe("exported spans are sanitized before they leave the browser", () => {
	async function exportProbe(options: Partial<BrowserTracingOptions> = {}) {
		const { url, payload } = await collector();
		const tracing = installBrowserTracing({
			allowedAttributeKeys: ["app.screen", "url.full", "http.request.body"],
			allowedSpanNames: ["page.load"],
			resourceAttributes: {
				"user_agent.original": CANARY.userAgent,
				"service.version": "1.2.3",
				"host.name": CANARY.peer,
			},
			...options,
			endpoint: url,
		});
		const tracer = trace.getTracer("probe");

		const call = tracer.startSpan(`GET /items?token=${CANARY.query}`);
		call.setAttributes({
			"http.request.method": "GET",
			"http.response.status_code": 500,
			"url.full": `https://app.example.com/items?token=${CANARY.query}`,
			"url.query": `token=${CANARY.query}`,
			"url.path": `/items?token=${CANARY.query}`,
			"url.scheme": "https",
			"server.address": "app.example.com",
			"server.port": 443,
			"http.request.body": CANARY.body,
			"http.request.body.content": CANARY.body,
			"http.response.body": CANARY.body,
			"user_agent.original": CANARY.userAgent,
			"client.address": CANARY.peer,
			"network.peer.address": CANARY.peer,
			"app.screen": "settings",
			"app.unlisted": CANARY.body,
		});
		const failure = new TypeError(CANARY.exception);
		call.recordException(failure);
		call.setStatus({ code: SpanStatusCode.ERROR, message: CANARY.status });
		call.end();

		const page = tracer.startSpan("page.load");
		page.setAttribute(
			"app.screen",
			"x".repeat(TRACE_ATTRIBUTE_VALUE_MAX_LENGTH + 1),
		);
		page.addEvent(CANARY.name, { "app.screen": "home" });
		page.end();

		tracer.startSpan("GET").end();
		void tracing.flush();
		return payload;
	}

	it("puts no canary anywhere in the serialized OTLP payload", async () => {
		const body = await exportProbe();
		for (const canary of Object.values(CANARY)) {
			expect(body).not.toContain(canary);
		}
	});

	it("keeps only allowed keys, the transport set, a query-less path and the exception type", async () => {
		const payload = JSON.parse(await exportProbe());
		expect(payload.resourceSpans).toHaveLength(1);
		const resourceSpans = payload.resourceSpans[0];
		expect(attributeKeys(resourceSpans.resource.attributes)).toEqual(
			expect.not.arrayContaining(["user_agent.original", "host.name"]),
		);
		const resourceKeys = attributeKeys(resourceSpans.resource.attributes);
		expect(resourceKeys).toContain("service.name");
		expect(resourceKeys).toContain("service.version");

		const spans: OtlpSpan[] = resourceSpans.scopeSpans.flatMap(
			(scope: { spans: OtlpSpan[] }) => scope.spans,
		);
		const [call, page, bare] = spans;
		expect(call?.name).toBe(TRACE_SPAN_NAME_PLACEHOLDER);
		expect(attributeKeys(call?.attributes ?? [])).toEqual([
			"app.screen",
			"http.request.method",
			"http.response.status_code",
			"server.address",
			"server.port",
			"url.path",
			"url.scheme",
		]);
		expect(
			call?.attributes.find((attribute) => attribute.key === "url.path")?.value,
		).toEqual({ stringValue: "/items" });
		expect(call?.status.message ?? "").toBe("");
		expect(call?.events).toHaveLength(1);
		expect(call?.events[0]?.name).toBe("exception");
		expect(attributeKeys(call?.events[0]?.attributes ?? [])).toEqual([
			"exception.type",
		]);

		expect(page?.name).toBe("page.load");
		expect(page?.attributes).toEqual([]);
		expect(page?.events[0]?.name).toBe(TRACE_SPAN_NAME_PLACEHOLDER);
		expect(bare?.name).toBe("GET");
	});
});

describe("OpenTelemetry API registration", () => {
	it("pins the exact @opentelemetry/api version the package resolves", () => {
		// Resolved from this file, not the working directory: the package's own
		// run and the frontend's root run start from different directories.
		const manifestPath = join(__dirname, "..", "..", "..", "package.json");
		const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as {
			dependencies: Record<string, string>;
		};
		// The package's `exports` hide its manifest; walk up from its entry.
		const entry = createRequire(manifestPath).resolve("@opentelemetry/api");
		const apiRoot = entry.slice(
			0,
			entry.lastIndexOf("@opentelemetry/api") + "@opentelemetry/api".length,
		);
		const resolved = JSON.parse(
			readFileSync(join(apiRoot, "package.json"), "utf8"),
		) as { version: string };
		expect(manifest.dependencies["@opentelemetry/api"]).toBe(OTEL_API_VERSION);
		expect(resolved.version).toBe(OTEL_API_VERSION);
	});

	for (const order of ["first, second", "second, first"] as const) {
		it(`shares one install across independently loaded copies (${order})`, async () => {
			vi.resetModules();
			const first = await import("../tracing.js");
			vi.resetModules();
			const second = await import("../tracing.js");
			expect(second).not.toBe(first);

			const [leader, follower] =
				order === "first, second" ? [first, second] : [second, first];
			const owned = leader.installBrowserTracing({
				...NO_KEYS,
				endpoint: null,
			});
			const joined = follower.installBrowserTracing({
				...NO_KEYS,
				endpoint: "https://collector.example.com/v1/traces",
			});
			expect(owned.status).toBe("registered");
			expect(joined).toBe(owned);
			expect(trace.getTracer("t").startSpan("s").isRecording()).toBe(true);
		});
	}

	it("reuses a compatible provider someone else registered", () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const foreign = new BasicTracerProvider();
		expect(trace.setGlobalTracerProvider(foreign)).toBe(true);
		const tracing = installBrowserTracing({ ...NO_KEYS, endpoint: null });
		expect(tracing.status).toBe("reused");
		expect(tracing.registered).toBe(false);
		expect(installBrowserTracing(NO_KEYS)).toBe(tracing);
		expect(warn).not.toHaveBeenCalled();
	});

	describe("against a registry from another API version", () => {
		const global = globalThis as unknown as Registry;
		let original: unknown;

		function stubRegistry(registry: Record<string, unknown>) {
			original = global[OTEL_API_GLOBAL];
			global[OTEL_API_GLOBAL] = registry;
		}

		afterEach(() => {
			global[OTEL_API_GLOBAL] = original;
		});

		it("reports a patch mismatch, warns once, caches nothing, and retries", () => {
			const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
			stubRegistry({ version: "1.9.0" });

			const first = installBrowserTracing({ ...NO_KEYS, endpoint: null });
			const second = installBrowserTracing({ ...NO_KEYS, endpoint: null });
			expect(first.status).toBe("incompatible");
			expect(second.status).toBe("incompatible");
			expect(second).not.toBe(first);
			expect(warn).toHaveBeenCalledTimes(1);
			expect(String(warn.mock.calls[0]?.[0])).toContain(OTEL_API_VERSION);

			global[OTEL_API_GLOBAL] = original;
			const retried = installBrowserTracing({ ...NO_KEYS, endpoint: null });
			expect(retried.status).toBe("registered");
		});

		it("does not reuse a provider registered by an incompatible API", () => {
			vi.spyOn(console, "warn").mockImplementation(() => {});
			// The API accepts a newer minor on the page, never an older one.
			stubRegistry({ version: "1.8.0", trace: new BasicTracerProvider() });
			const tracing = installBrowserTracing({ ...NO_KEYS, endpoint: null });
			expect(tracing.status).toBe("incompatible");
		});
	});
});

describe("the trace-export meta", () => {
	function meta(parent: HTMLElement, content: string) {
		const element = document.createElement("meta");
		element.name = TRACE_EXPORT_META_NAME;
		element.content = content;
		parent.append(element);
	}

	it("ignores a meta injected into the body, with one warning", () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		meta(document.body, `${location.origin}/v1/traces`);
		expect(resolveTraceExportEndpoint()).toBeNull();
		expect(resolveTraceExportEndpoint()).toBeNull();
		expect(warn).toHaveBeenCalledTimes(1);
		const tracing = installBrowserTracing(NO_KEYS);
		expect(tracing.endpoint).toBeNull();
	});

	it("rejects a head meta naming an origin off the allowlist", () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		meta(document.head, "https://collector.attacker.example/v1/traces");
		expect(
			resolveTraceExportEndpoint(undefined, ["https://collector.example.com"]),
		).toBeNull();
		expect(warn).toHaveBeenCalledTimes(1);
	});

	it("rejects a non-HTTP endpoint", () => {
		vi.spyOn(console, "warn").mockImplementation(() => {});
		meta(document.head, "javascript:alert(1)");
		expect(resolveTraceExportEndpoint()).toBeNull();
	});

	it("accepts a same-origin head meta", () => {
		meta(document.head, "/v1/traces");
		expect(resolveTraceExportEndpoint()).toBe("/v1/traces");
	});

	it("accepts an explicitly allowlisted origin", () => {
		meta(document.head, "http://localhost:4318/v1/traces");
		expect(
			resolveTraceExportEndpoint(undefined, ["http://localhost:4318"]),
		).toBe("http://localhost:4318/v1/traces");
		const tracing = installBrowserTracing({
			...NO_KEYS,
			allowedEndpointOrigins: ["http://localhost:4318/"],
		});
		expect(tracing.endpoint).toBe("http://localhost:4318/v1/traces");
	});

	it("accepts an origin allowlisted at build time", () => {
		vi.stubEnv("PUBLIC_TRACE_EXPORT_ALLOWED_ORIGINS", "http://localhost:4318");
		meta(document.head, "http://localhost:4318/v1/traces");
		expect(resolveTraceExportEndpoint()).toBe(
			"http://localhost:4318/v1/traces",
		);
	});
});
