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

/**
 * The `<meta name>` a host page sets to name the browser's trace-export
 * destination. Read at install time; a page without it falls back to the
 * build-time value, and without that the browser only propagates.
 */
export const TRACE_EXPORT_META_NAME = "trace-export";

/**
 * The build-time fallback. A bundler that substitutes `process.env.PUBLIC_*`
 * (rsbuild, Next's `NEXT_PUBLIC_*` aside) inlines it into the consumer's
 * bundle; an unsubstituted reference is simply absent.
 */
export const TRACE_EXPORT_BUILD_ENV = "PUBLIC_TRACE_EXPORT_URL";

/** How long finished spans wait before a batch is exported. */
export const TRACE_EXPORT_DELAY_MS = 100;

const INSTALL_KEY = Symbol.for("@codefly-dev/saas-ui/browser-tracing");

// The interceptor's propagator: trace context only. It never reads or writes
// `baggage`, whatever propagator someone else may have registered globally.
const traceContextOnly = new W3CTraceContextPropagator();

const headerSetter: TextMapSetter<Headers> = {
	set: (carrier, key, value) => carrier.set(key, value),
};

export interface BrowserTracingOptions {
	/**
	 * The OTLP/HTTP traces URL. Given, it wins over the page and the build;
	 * `null` forces propagation only. Omitted, the `<meta name="trace-export">`
	 * content, then the build-time `PUBLIC_TRACE_EXPORT_URL`, decide.
	 */
	endpoint?: string | null;
	/** `service.name` on exported spans. */
	serviceName?: string;
	/** Further resource attributes (version, environment, schema version). */
	resourceAttributes?: Record<string, string | number | boolean>;
}

export interface BrowserTracing {
	/** Whether this call registered the provider (false: someone else owns it). */
	readonly registered: boolean;
	/** The destination spans export to, or null when the browser only propagates. */
	readonly endpoint: string | null;
	/** Exports what is buffered now; resolves even when nothing exports. */
	flush(): Promise<void>;
}

type Installed = { [INSTALL_KEY]?: BrowserTracing };

/**
 * Registers the browser's tracer provider, once per page.
 *
 * Idempotent across every copy of this package on the page (a host and each
 * remote may bundle their own): the first caller wins, and later callers get
 * the first caller's handle back with their options ignored. When another
 * library already registered a global tracer provider, nothing is replaced and
 * the handle reports `registered: false`.
 *
 * What it registers: W3C trace context as the only propagator (never baggage),
 * a sampler that always records a root, and — only when an endpoint resolves —
 * an OTLP/HTTP exporter behind a 100 ms batch that flushes when the page is
 * hidden or unloaded. With no endpoint, spans are created and their context
 * propagates, but nothing leaves the browser.
 */
export function installBrowserTracing(
	options: BrowserTracingOptions = {},
): BrowserTracing {
	const global = globalThis as Installed;
	const existing = global[INSTALL_KEY];
	if (existing) return existing;

	const endpoint = resolveTraceExportEndpoint(options.endpoint);
	const provider = new WebTracerProvider({
		resource: resourceFromAttributes({
			...options.resourceAttributes,
			"service.name": options.serviceName ?? "browser",
		}),
		sampler: new ParentBasedSampler({ root: new AlwaysOnSampler() }),
		spanProcessors: endpoint
			? [
					new BatchSpanProcessor(new OTLPTraceExporter({ url: endpoint }), {
						scheduledDelayMillis: TRACE_EXPORT_DELAY_MS,
					}),
				]
			: [],
	});

	if (!trace.setGlobalTracerProvider(provider)) {
		void provider.shutdown();
		const foreign: BrowserTracing = {
			registered: false,
			endpoint: null,
			flush: async () => {},
		};
		global[INSTALL_KEY] = foreign;
		return foreign;
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

	const handle: BrowserTracing = { registered: true, endpoint, flush };
	global[INSTALL_KEY] = handle;
	return handle;
}

/** Where spans export: explicit option, then the page's meta, then the build. */
export function resolveTraceExportEndpoint(
	explicit?: string | null,
): string | null {
	if (explicit !== undefined) return explicit?.trim() || null;
	const fromPage =
		typeof document === "undefined"
			? undefined
			: document
					.querySelector(`meta[name="${TRACE_EXPORT_META_NAME}"]`)
					?.getAttribute("content")
					?.trim();
	if (fromPage) return fromPage;
	return buildTimeEndpoint()?.trim() || null;
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
	delete (globalThis as Installed)[INSTALL_KEY];
	trace.disable();
	propagation.disable();
	context.disable();
}
