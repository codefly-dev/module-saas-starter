import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { AccessibleScopeService } from "@codefly-dev/saas-sdk";
import { createClient } from "@connectrpc/connect";
import { context, propagation, trace } from "@opentelemetry/api";
import { afterEach, describe, expect, it, vi } from "vitest";
import { solutionTransport } from "../index.js";
import {
	installBrowserTracing,
	resetBrowserTracingForTests,
	resolveTraceExportEndpoint,
	TRACE_EXPORT_META_NAME,
} from "../tracing.js";

const NO_KEYS = { allowedAttributeKeys: [] };
const TRACEPARENT = /^00-([0-9a-f]{32})-([0-9a-f]{16})-0[01]$/;
const CALLER_TRACEPARENT =
	"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";

function scopesFetch() {
	return vi.fn<typeof fetch>(async () => Response.json({ scopes: [] }));
}

function client(apiBase: string, authedFetch: typeof fetch) {
	return createClient(
		AccessibleScopeService,
		solutionTransport({ apiBase, getAccessToken: () => "viewer", authedFetch }),
	);
}

const request = { orgId: "o1", resourceType: "documents", action: "read" };

function sentHeaders(mock: ReturnType<typeof scopesFetch>): Headers {
	return new Headers(mock.mock.calls[0]![1]?.headers);
}

/** Calls `fn` with `span` active — the interceptor reads it synchronously. */
function underSpan<T>(fn: () => T) {
	const span = trace.getTracer("test").startSpan("interaction");
	const result = context.with(trace.setSpan(context.active(), span), fn);
	return { span, result };
}

afterEach(() => {
	resetBrowserTracingForTests();
	for (const meta of document.head.querySelectorAll("meta")) meta.remove();
	vi.restoreAllMocks();
});

describe("installBrowserTracing", () => {
	it("is idempotent: the first caller wins and later options are ignored", () => {
		const first = installBrowserTracing({ ...NO_KEYS, endpoint: null });
		const second = installBrowserTracing({
			...NO_KEYS,
			endpoint: "https://collector.example.com/v1/traces",
		});
		expect(second).toBe(first);
		expect(first.registered).toBe(true);
		expect(first.status).toBe("registered");
		expect(second.endpoint).toBeNull();
	});

	it("registers W3C trace context only, never baggage", () => {
		installBrowserTracing({ ...NO_KEYS, endpoint: null });
		expect(propagation.fields().sort()).toEqual(["traceparent", "tracestate"]);
	});

	it("records roots and propagates without exporting when no endpoint resolves", async () => {
		const send = vi.spyOn(globalThis, "fetch");
		const beacon = vi.fn(() => true);
		Object.defineProperty(navigator, "sendBeacon", {
			value: beacon,
			configurable: true,
		});
		const tracing = installBrowserTracing(NO_KEYS);
		expect(tracing.endpoint).toBeNull();

		const span = trace.getTracer("test").startSpan("root");
		expect(span.isRecording()).toBe(true);
		expect(span.spanContext().traceFlags & 1).toBe(1);
		span.end();
		await tracing.flush();
		expect(send).not.toHaveBeenCalled();
		expect(beacon).not.toHaveBeenCalled();
	});

	it("exports to the endpoint when one resolves", async () => {
		// A real local collector: the test environment resolves the exporter's
		// Node transport, so a stubbed browser fetch would never see the batch.
		const received = new Promise<string>((resolveRequest) => {
			const server = createServer((req, res) => {
				res.writeHead(200).end();
				server.close();
				resolveRequest(req.url ?? "");
			});
			server.listen(0, "127.0.0.1", () => {
				const { port } = server.address() as AddressInfo;
				const tracing = installBrowserTracing({
					...NO_KEYS,
					endpoint: `http://127.0.0.1:${port}/v1/traces`,
				});
				trace.getTracer("test").startSpan("root").end();
				void tracing.flush();
			});
		});
		expect(await received).toBe("/v1/traces");
	});
});

describe("resolveTraceExportEndpoint", () => {
	it("prefers the explicit option, then the page's meta, else nothing", () => {
		expect(resolveTraceExportEndpoint()).toBeNull();
		const meta = document.createElement("meta");
		meta.name = TRACE_EXPORT_META_NAME;
		meta.content = ` ${location.origin}/v1/traces `;
		document.head.append(meta);
		expect(resolveTraceExportEndpoint()).toBe(`${location.origin}/v1/traces`);
		expect(resolveTraceExportEndpoint(null)).toBeNull();
		expect(resolveTraceExportEndpoint("https://other.example.com/t")).toBe(
			"https://other.example.com/t",
		);
	});
});

describe("solutionTransport trace context", () => {
	it("injects the active span's traceparent on a same-origin call that has none", async () => {
		installBrowserTracing({ ...NO_KEYS, endpoint: null });
		const authedFetch = scopesFetch();
		const { span, result } = underSpan(() =>
			client("/api/solutions/s/proxy", authedFetch).listMyAccessibleScopes(
				request,
			),
		);
		await result;
		const headers = sentHeaders(authedFetch);
		const match = TRACEPARENT.exec(headers.get("traceparent") ?? "");
		expect(match?.[1]).toBe(span.spanContext().traceId);
		expect(match?.[2]).toBe(span.spanContext().spanId);
		expect(headers.has("baggage")).toBe(false);
	});

	it("leaves a traceparent the caller already set untouched", async () => {
		installBrowserTracing({ ...NO_KEYS, endpoint: null });
		const authedFetch = scopesFetch();
		const { result } = underSpan(() =>
			client("/api", authedFetch).listMyAccessibleScopes(request, {
				headers: { traceparent: CALLER_TRACEPARENT },
			}),
		);
		await result;
		expect(sentHeaders(authedFetch).get("traceparent")).toBe(
			CALLER_TRACEPARENT,
		);
	});

	it("never sends trace context to another origin", async () => {
		installBrowserTracing({ ...NO_KEYS, endpoint: null });
		const authedFetch = scopesFetch();
		const { result } = underSpan(() =>
			client(
				"https://elsewhere.example.com/api",
				authedFetch,
			).listMyAccessibleScopes(request),
		);
		await result;
		expect(sentHeaders(authedFetch).has("traceparent")).toBe(false);
	});

	it("adds nothing when tracing was never installed", async () => {
		const authedFetch = scopesFetch();
		const { result } = underSpan(() =>
			client("/api", authedFetch).listMyAccessibleScopes(request),
		);
		await result;
		const headers = sentHeaders(authedFetch);
		expect(headers.has("traceparent")).toBe(false);
		expect(headers.has("tracestate")).toBe(false);
		expect(headers.has("baggage")).toBe(false);
	});

	it("never sets baggage, even when the active context carries some", async () => {
		installBrowserTracing({ ...NO_KEYS, endpoint: null });
		const authedFetch = scopesFetch();
		const span = trace.getTracer("test").startSpan("interaction");
		const withBaggage = propagation.setBaggage(
			trace.setSpan(context.active(), span),
			propagation.createBaggage({ tenant: { value: "forged" } }),
		);
		await context.with(withBaggage, () =>
			client("/api", authedFetch).listMyAccessibleScopes(request),
		);
		const headers = sentHeaders(authedFetch);
		expect(headers.has("traceparent")).toBe(true);
		expect(headers.has("baggage")).toBe(false);
	});
});
