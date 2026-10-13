import {
	type Context,
	context,
	propagation,
	type TextMapSetter,
	trace,
} from "@opentelemetry/api";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { resourceFromAttributes } from "@opentelemetry/resources";
import {
	AlwaysOnSampler,
	BatchSpanProcessor,
	ParentBasedSampler,
	StackContextManager,
	WebTracerProvider,
} from "@opentelemetry/sdk-trace-web";

import { SanitizingSpanExporter } from "./trace-sanitizer.js";

/**
 * The `<meta name>` a host page sets in its `<head>` to name the browser's
 * trace-export destination. Read at install time and honoured only when its
 * URL's origin is allowed (the page's own, or one the installer lists); a page
 * without it falls back to the build-time value, and without that the browser
 * only propagates.
 */
export const TRACE_EXPORT_META_NAME = "trace-export";

/**
 * The build-time fallback. A bundler that substitutes `process.env.PUBLIC_*`
 * (rsbuild, Next's `NEXT_PUBLIC_*` aside) inlines it into the consumer's
 * bundle; an unsubstituted reference is simply absent.
 */
export const TRACE_EXPORT_BUILD_ENV = "PUBLIC_TRACE_EXPORT_URL";

/**
 * Build-time, comma-separated origins a `<meta name="trace-export">` may
 * name, beyond the page's own. Substituted like {@link TRACE_EXPORT_BUILD_ENV}.
 */
export const TRACE_EXPORT_ALLOWED_ORIGINS_BUILD_ENV =
	"PUBLIC_TRACE_EXPORT_ALLOWED_ORIGINS";

/** How long finished spans wait before a batch is exported. */
export const TRACE_EXPORT_DELAY_MS = 100;

/**
 * The exact `@opentelemetry/api` version this package is built against. The
 * API registers its globals only when every copy on the page is this exact
 * version, so every bundle on a page that shares a tracer provider must
 * resolve `@opentelemetry/api` to it.
 */
export const OTEL_API_VERSION = "1.9.1";

const OTEL_API_GLOBAL = Symbol.for(
	`opentelemetry.js.api.${OTEL_API_VERSION.split(".")[0]}`,
);
const INSTALL_KEY = Symbol.for("@codefly-dev/saas-ui/browser-tracing");
const WARNED_KEY = Symbol.for("@codefly-dev/saas-ui/browser-tracing.warned");

const WARN_INCOMPATIBLE = "incompatible-api";
const WARN_META = "meta";

// The interceptor's propagator: trace context only. It never reads or writes
// `baggage`, whatever propagator someone else may have registered globally.
const traceContextOnly = new W3CTraceContextPropagator();

const headerSetter: TextMapSetter<Headers> = {
	set: (carrier, key, value) => carrier.set(key, value),
};

export interface BrowserTracingOptions {
	/**
	 * Span attribute keys that may be exported, beyond the built-in transport
	 * set (method, status code, query-less path, scheme, server address and
	 * port). Keys on the built-in deny list (full URLs, queries, user agent,
	 * peer addresses, exception text, bodies) never export, even if listed.
	 */
	allowedAttributeKeys: readonly string[];
	/**
	 * Span names that may be exported. Any name that is neither listed here
	 * nor a bare HTTP method exports as a fixed placeholder — and omitting
	 * this vouches for no name at all, because a span name is free-form text
	 * that carries whatever its instrumentation author put there.
	 */
	allowedSpanNames?: readonly string[];
	/**
	 * Built-in transport keys to withhold. Use it to drop `url.path` when path
	 * segments carry identifiers (`/accounts/<iban>`), without giving up the
	 * method, status and server attributes beside it.
	 */
	deniedAttributeKeys?: readonly string[];
	/**
	 * Origins a `<meta name="trace-export">` may name, beyond the page's own
	 * (e.g. `http://localhost:4318`). Merged with the build-time
	 * `PUBLIC_TRACE_EXPORT_ALLOWED_ORIGINS`.
	 */
	allowedEndpointOrigins?: readonly string[];
	/**
	 * The OTLP/HTTP traces URL. Given, it wins over the page and the build;
	 * `null` forces propagation only. Omitted, the `<meta name="trace-export">`
	 * content, then the build-time `PUBLIC_TRACE_EXPORT_URL`, decide.
	 */
	endpoint?: string | null;
	/** `service.name` on exported spans. */
	serviceName?: string;
	/**
	 * Further resource attributes. Only `service.version` and
	 * `deployment.environment.name` are exported; the rest stay on the page.
	 */
	resourceAttributes?: Record<string, string | number | boolean>;
}

/**
 * What an install did:
 * - `registered` — this package's provider now owns the page;
 * - `reused` — a compatible provider was already registered and is used as is;
 * - `incompatible` — the page's OpenTelemetry API is another version, or
 *   registration failed. Nothing is cached: a later call tries again.
 */
export type BrowserTracingStatus = "registered" | "reused" | "incompatible";

export interface BrowserTracing {
	readonly status: BrowserTracingStatus;
	/** Whether this package's provider owns the page (`status === "registered"`). */
	readonly registered: boolean;
	/** The destination spans export to, or null when the browser only propagates. */
	readonly endpoint: string | null;
	/** Exports what is buffered now; resolves even when nothing exports. */
	flush(): Promise<void>;
}

type OtelApiRegistry = { version?: unknown; trace?: unknown };

type TracingGlobals = {
	[INSTALL_KEY]?: BrowserTracing;
	[WARNED_KEY]?: Set<string>;
	[OTEL_API_GLOBAL]?: OtelApiRegistry;
};

/**
 * Registers the browser's tracer provider, once per page.
 *
 * Idempotent across every copy of this package on the page (a host and each
 * remote may bundle their own): the first caller wins, and later callers get
 * the first caller's handle back with their options ignored. When a
 * compatible provider is already registered by someone else, it is reused
 * (`status: "reused"`). When the page's OpenTelemetry API is a different
 * version — the API refuses to register across even a patch difference — the
 * handle reports `status: "incompatible"`, one warning is logged, and nothing
 * is cached, so a later call can succeed.
 *
 * What it registers: W3C trace context as the only propagator (never baggage),
 * a sampler that always records a root, and — only when an endpoint resolves —
 * an OTLP/HTTP exporter behind a 100 ms batch that flushes when the page is
 * hidden or unloaded. Every span passes through a sanitizing exporter before
 * it is serialized: only allowed attribute keys survive, the resource is
 * rebuilt from an allowlist, status descriptions are emptied, and exception
 * events keep only their type. With no endpoint, spans are created and their
 * context propagates, but nothing leaves the browser.
 */
export function installBrowserTracing(
	options: BrowserTracingOptions,
): BrowserTracing {
	const global = globalThis as TracingGlobals;
	const existing = global[INSTALL_KEY];
	if (existing) return existing;

	const registry = global[OTEL_API_GLOBAL];
	if (registry?.trace) {
		// The API hands back a registered provider only when its version is
		// compatible with ours; otherwise it falls back to its own proxy.
		if ((trace.getTracerProvider() as unknown) === registry.trace) {
			const reused: BrowserTracing = {
				status: "reused",
				registered: false,
				endpoint: null,
				flush: async () => {},
			};
			global[INSTALL_KEY] = reused;
			return reused;
		}
		return incompatible(registry.version);
	}
	if (registry && registry.version !== OTEL_API_VERSION) {
		return incompatible(registry.version);
	}

	const endpoint = resolveTraceExportEndpoint(
		options.endpoint,
		options.allowedEndpointOrigins,
	);
	const provider = new WebTracerProvider({
		resource: resourceFromAttributes({
			...options.resourceAttributes,
			"service.name": options.serviceName ?? "browser",
		}),
		sampler: new ParentBasedSampler({ root: new AlwaysOnSampler() }),
		spanProcessors: endpoint
			? [
					new BatchSpanProcessor(
						new SanitizingSpanExporter(
							new OTLPTraceExporter({ url: endpoint }),
							{
								allowedAttributeKeys: options.allowedAttributeKeys,
								allowedSpanNames: options.allowedSpanNames,
								deniedAttributeKeys: options.deniedAttributeKeys,
							},
						),
						{ scheduledDelayMillis: TRACE_EXPORT_DELAY_MS },
					),
				]
			: [],
	});

	if (!trace.setGlobalTracerProvider(provider)) {
		void provider.shutdown();
		return incompatible(global[OTEL_API_GLOBAL]?.version);
	}
	propagation.setGlobalPropagator(traceContextOnly);
	context.setGlobalContextManager(new StackContextManager().enable());

	const flush = () => provider.forceFlush().catch(() => {});
	if (endpoint && typeof window !== "undefined") {
		// The batch processor already flushes on `visibilitychange`; `pagehide`
		// covers the unloads that never report hidden first.
		window.addEventListener("visibilitychange", () => {
			if (document.visibilityState === "hidden") void flush();
		});
		window.addEventListener("pagehide", () => void flush());
	}

	const handle: BrowserTracing = {
		status: "registered",
		registered: true,
		endpoint,
		flush,
	};
	global[INSTALL_KEY] = handle;
	return handle;
}

/** Not cached: the next install call checks the page again. */
function incompatible(version: unknown): BrowserTracing {
	warnOnce(
		WARN_INCOMPATIBLE,
		`@codefly-dev/saas-ui: browser tracing not installed — the page's @opentelemetry/api is v${String(version)}, this package needs exactly v${OTEL_API_VERSION}. Every bundle on the page must share one @opentelemetry/api version.`,
	);
	return {
		status: "incompatible",
		registered: false,
		endpoint: null,
		flush: async () => {},
	};
}

function warnOnce(key: string, message: string): void {
	const global = globalThis as TracingGlobals;
	const warned = global[WARNED_KEY] ?? new Set<string>();
	global[WARNED_KEY] = warned;
	if (warned.has(key)) return;
	warned.add(key);
	console.warn(message);
}

/**
 * Where spans export: the explicit option, then the page's `<head>` meta,
 * then the build.
 *
 * The meta is honoured only as a direct child of `<head>` and only when its
 * URL's origin is the page's own or in `allowedEndpointOrigins` (plus the
 * build-time `PUBLIC_TRACE_EXPORT_ALLOWED_ORIGINS`). A meta elsewhere in the
 * document, or naming another origin, is ignored with one warning and the
 * browser only propagates.
 */
export function resolveTraceExportEndpoint(
	explicit?: string | null,
	allowedEndpointOrigins: readonly string[] = [],
): string | null {
	if (explicit !== undefined) return explicit?.trim() || null;
	if (typeof document !== "undefined") {
		const fromHead = headMetaContent();
		if (fromHead !== undefined) {
			if (isAllowedEndpoint(fromHead, allowedEndpointOrigins)) return fromHead;
			warnOnce(
				WARN_META,
				`@codefly-dev/saas-ui: ignoring <meta name="${TRACE_EXPORT_META_NAME}">: its origin is not allowed; spans will not be exported.`,
			);
			return null;
		}
		if (document.querySelector(`meta[name="${TRACE_EXPORT_META_NAME}"]`)) {
			warnOnce(
				WARN_META,
				`@codefly-dev/saas-ui: ignoring <meta name="${TRACE_EXPORT_META_NAME}"> outside <head>; spans will not be exported.`,
			);
			return null;
		}
	}
	return buildTimeEndpoint()?.trim() || null;
}

function headMetaContent(): string | undefined {
	const head = document.head;
	if (!head) return undefined;
	for (const child of Array.from(head.children)) {
		if (
			child.tagName === "META" &&
			child.getAttribute("name") === TRACE_EXPORT_META_NAME
		) {
			return child.getAttribute("content")?.trim() ?? "";
		}
	}
	return undefined;
}

function isAllowedEndpoint(
	value: string,
	allowedEndpointOrigins: readonly string[],
): boolean {
	if (typeof location === "undefined" || !value) return false;
	let url: URL;
	try {
		url = new URL(value, location.href);
	} catch {
		return false;
	}
	if (url.protocol !== "https:" && url.protocol !== "http:") return false;
	const allowed = new Set([location.origin]);
	for (const origin of [
		...allowedEndpointOrigins,
		...buildTimeAllowedOrigins(),
	]) {
		const normalized = originOf(origin);
		if (normalized) allowed.add(normalized);
	}
	return allowed.has(url.origin);
}

function originOf(value: string): string | null {
	try {
		const origin = new URL(value.trim()).origin;
		return origin === "null" ? null : origin;
	} catch {
		return null;
	}
}

declare const process: { env: Record<string, string | undefined> };

function buildTimeEndpoint(): string | undefined {
	try {
		// Spelled out literally so a bundler's define can replace it.
		return process.env.PUBLIC_TRACE_EXPORT_URL;
	} catch {
		return undefined;
	}
}

function buildTimeAllowedOrigins(): string[] {
	let raw: string | undefined;
	try {
		// Spelled out literally so a bundler's define can replace it.
		raw = process.env.PUBLIC_TRACE_EXPORT_ALLOWED_ORIGINS;
	} catch {
		return [];
	}
	return (raw ?? "").split(",").filter((origin) => origin.trim());
}

/**
 * Stamps `traceparent` (and `tracestate`) from `ctx` onto `headers` — unless
 * the request already carries one, or is not bound for this page's own origin.
 * The trace header never leaves for a third party. No baggage, ever.
 */
export function injectTraceContext(
	headers: Headers,
	url: string,
	ctx: Context = context.active(),
): void {
	if (headers.has("traceparent")) return;
	if (!isSameOrigin(url)) return;
	traceContextOnly.inject(ctx, headers, headerSetter);
}

function isSameOrigin(url: string): boolean {
	if (typeof location === "undefined") return false;
	try {
		return new URL(url, location.href).origin === location.origin;
	} catch {
		return false;
	}
}

/** Test seam: forget the install and unregister the globals. */
export function resetBrowserTracingForTests(): void {
	const global = globalThis as TracingGlobals;
	delete global[INSTALL_KEY];
	delete global[WARNED_KEY];
	trace.disable();
	propagation.disable();
	context.disable();
}
