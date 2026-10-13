import type { Attributes, AttributeValue, Link } from "@opentelemetry/api";
import type { ExportResult } from "@opentelemetry/core";
import {
	type Resource,
	resourceFromAttributes,
} from "@opentelemetry/resources";
import type {
	ReadableSpan,
	SpanExporter,
	TimedEvent,
} from "@opentelemetry/sdk-trace-web";

/**
 * The name a span (or event) takes when its own name is neither one the caller
 * allowed nor a bare HTTP method. A free-form name can carry a URL, an id or a
 * message, so it never leaves the browser unvetted.
 */
export const TRACE_SPAN_NAME_PLACEHOLDER = "span";

/**
 * The longest string an exported attribute value, span name or resource value
 * may be. Anything longer is dropped (attributes, resource) or replaced by the
 * placeholder (names): a long value is where payloads and tokens hide.
 */
export const TRACE_ATTRIBUTE_VALUE_MAX_LENGTH = 256;

/**
 * Transport attributes every exported span may keep, whatever the caller
 * allows: they describe the call, never its content. `url.path` is exported
 * with any query or fragment cut off.
 *
 * A path is the one entry here that can still carry content, because a path
 * SEGMENT is the caller's to shape: `/reset/tok_live_…/confirm` and
 * `/accounts/<iban>` export whole, query-stripping notwithstanding. Cutting it
 * from this set outright would cost every consumer its most useful diagnostic
 * for a feature that is off by default, so it stays — and a consumer that puts
 * identifiers in paths withholds it by naming it in
 * {@link TraceSanitizerPolicy.deniedAttributeKeys}.
 */
export const TRACE_STANDARD_ATTRIBUTE_KEYS: readonly string[] = [
	"http.request.method",
	"http.response.status_code",
	"url.path",
	"url.scheme",
	"server.address",
	"server.port",
];

/**
 * Keys that never leave the browser, even when a caller lists them: full URLs
 * and queries, the user agent, the peer's address, exception text and stacks.
 * Request and response bodies are matched by {@link isDeniedAttributeKey}.
 */
export const TRACE_DENIED_ATTRIBUTE_KEYS: readonly string[] = [
	"url.full",
	"url.query",
	"url.fragment",
	"user_agent.original",
	"client.address",
	"client.port",
	"exception.message",
	"exception.stacktrace",
	// Pre-1.0 semantic-convention spellings of the same data.
	"http.url",
	"http.target",
	"http.user_agent",
	"http.client_ip",
];

const DENIED_ATTRIBUTE_PREFIXES = ["network.peer.", "net.peer.", "enduser."];

/** `http.request.body`, `http.response.body.content`, … — not `….body.size`. */
const BODY_KEY = /(^|\.)body(\.|$)/;
const BODY_SIZE_KEY = /(^|\.)body\.size$/;

/** The resource keys an exported batch carries; the rest is rebuilt away. */
export const TRACE_RESOURCE_ATTRIBUTE_KEYS: readonly string[] = [
	"service.name",
	"service.version",
	"deployment.environment.name",
	"telemetry.sdk.name",
	"telemetry.sdk.language",
	"telemetry.sdk.version",
];

const EXCEPTION_EVENT_NAME = "exception";
const EXCEPTION_TYPE_KEY = "exception.type";
const URL_PATH_KEY = "url.path";

/** A bare HTTP method, as the HTTP semantic conventions name client spans. */
const STANDARD_HTTP_SPAN_NAME =
	/^(?:HTTP )?(?:GET|HEAD|POST|PUT|DELETE|CONNECT|OPTIONS|TRACE|PATCH)$/;

export function isDeniedAttributeKey(key: string): boolean {
	if (TRACE_DENIED_ATTRIBUTE_KEYS.includes(key)) return true;
	if (DENIED_ATTRIBUTE_PREFIXES.some((prefix) => key.startsWith(prefix))) {
		return true;
	}
	return BODY_KEY.test(key) && !BODY_SIZE_KEY.test(key);
}

export interface TraceSanitizerPolicy {
	/** Attribute keys the caller vouches for, beyond the standard transport set. */
	readonly allowedAttributeKeys: readonly string[];
	/**
	 * Span (and event) names the caller vouches for. Every name that is
	 * neither listed here nor a bare HTTP method becomes the placeholder.
	 *
	 * Omitting it vouches for NOTHING, which is the safe reading: a span name
	 * is free-form text an instrumentation author chose, so it carries
	 * whatever they put there — `"load user alice@example.com reset token
	 * tok_live_…"` is a name a reasonable tracer would produce. Stripping its
	 * query and checking its length, which is all an unvetted name used to
	 * get, does nothing about either of those. A caller that wants its own
	 * names exported lists them.
	 */
	readonly allowedSpanNames?: readonly string[];
	/**
	 * Keys to withhold even though they are standard transport attributes.
	 * Subtracts from {@link TRACE_STANDARD_ATTRIBUTE_KEYS}, so a consumer that
	 * puts identifiers in its paths can drop `url.path` without giving up the
	 * method, status and server attributes beside it.
	 */
	readonly deniedAttributeKeys?: readonly string[];
}

/**
 * Wraps the exporter that sends spans off the page, as the last step before
 * they are serialized. Contract-agnostic: it knows nothing of what any caller
 * records, only which keys the caller allowed. Every span is rebuilt from an
 * allowlist rather than scrubbed of a denylist, so an attribute nobody
 * anticipated is dropped, not exported.
 */
export class SanitizingSpanExporter implements SpanExporter {
	private readonly allowedKeys: ReadonlySet<string>;
	private readonly allowedNames: ReadonlySet<string>;
	// One sanitized resource per source resource, so a batch still groups
	// under a single resource when it is serialized.
	private readonly resources = new WeakMap<Resource, Resource>();

	constructor(
		private readonly inner: SpanExporter,
		policy: TraceSanitizerPolicy,
	) {
		const denied = new Set(policy.deniedAttributeKeys ?? []);
		this.allowedKeys = new Set(
			[...TRACE_STANDARD_ATTRIBUTE_KEYS, ...policy.allowedAttributeKeys].filter(
				(key) => !isDeniedAttributeKey(key) && !denied.has(key),
			),
		);
		// An absent allowlist vouches for no name at all. See allowedSpanNames.
		this.allowedNames = new Set(policy.allowedSpanNames ?? []);
	}

	export(
		spans: ReadableSpan[],
		resultCallback: (result: ExportResult) => void,
	): void {
		this.inner.export(
			spans.map((span) => this.sanitize(span)),
			resultCallback,
		);
	}

	shutdown(): Promise<void> {
		return this.inner.shutdown();
	}

	forceFlush(): Promise<void> {
		return this.inner.forceFlush?.() ?? Promise.resolve();
	}

	/** The span as it may leave the browser. */
	sanitize(span: ReadableSpan): ReadableSpan {
		const spanContext = span.spanContext();
		return {
			name: this.name(span.name),
			kind: span.kind,
			spanContext: () => spanContext,
			parentSpanContext: span.parentSpanContext,
			startTime: span.startTime,
			endTime: span.endTime,
			duration: span.duration,
			ended: span.ended,
			status: { code: span.status.code, message: "" },
			attributes: this.attributes(span.attributes),
			links: span.links.map(
				(link): Link => ({
					context: link.context,
					attributes: this.attributes(link.attributes ?? {}),
				}),
			),
			events: span.events.map((event) => this.event(event)),
			resource: this.resource(span.resource),
			instrumentationScope: span.instrumentationScope,
			droppedAttributesCount: span.droppedAttributesCount,
			droppedEventsCount: span.droppedEventsCount,
			droppedLinksCount: span.droppedLinksCount,
		};
	}

	private resource(source: Resource): Resource {
		let sanitized = this.resources.get(source);
		if (!sanitized) {
			sanitized = sanitizeResource(source);
			this.resources.set(source, sanitized);
		}
		return sanitized;
	}

	private name(name: string): string {
		if (STANDARD_HTTP_SPAN_NAME.test(name)) return name;
		return this.allowedNames.has(name) ? name : TRACE_SPAN_NAME_PLACEHOLDER;
	}

	private attributes(source: Attributes): Attributes {
		const kept: Attributes = {};
		for (const [key, value] of Object.entries(source)) {
			if (!this.allowedKeys.has(key)) continue;
			const safe = boundedValue(
				key === URL_PATH_KEY && typeof value === "string"
					? stripQuery(value)
					: value,
			);
			if (safe !== undefined) kept[key] = safe;
		}
		return kept;
	}

	private event(event: TimedEvent): TimedEvent {
		if (event.name === EXCEPTION_EVENT_NAME) {
			const type = boundedValue(event.attributes?.[EXCEPTION_TYPE_KEY]);
			return {
				time: event.time,
				name: EXCEPTION_EVENT_NAME,
				attributes:
					typeof type === "string" ? { [EXCEPTION_TYPE_KEY]: type } : {},
				droppedAttributesCount: event.droppedAttributesCount,
			};
		}
		return {
			time: event.time,
			name: this.name(event.name),
			attributes: this.attributes(event.attributes ?? {}),
			droppedAttributesCount: event.droppedAttributesCount,
		};
	}
}

/** The resource rebuilt from its allowlist: no user agent, no host details. */
export function sanitizeResource(resource: Resource): Resource {
	const kept: Attributes = {};
	for (const key of TRACE_RESOURCE_ATTRIBUTE_KEYS) {
		const value = boundedValue(resource.attributes[key]);
		if (value !== undefined) kept[key] = value;
	}
	return resourceFromAttributes(kept);
}

function stripQuery(value: string): string {
	const cut = value.search(/[?#]/);
	return cut === -1 ? value : value.slice(0, cut);
}

/** Primitive values within the length cap; an over-long or odd value is dropped. */
function boundedValue(
	value: AttributeValue | undefined,
): AttributeValue | undefined {
	if (typeof value === "number" || typeof value === "boolean") return value;
	if (typeof value === "string") {
		return value.length <= TRACE_ATTRIBUTE_VALUE_MAX_LENGTH ? value : undefined;
	}
	if (Array.isArray(value)) {
		// Every element must be a primitive the attribute type actually allows.
		// Measuring String(item) alone let a payload of any size through: an
		// object stringifies to "[object Object]", 15 characters, so
		// ["{a card number and a CVV}"] sat far under the cap and exported whole.
		// The cap has to bind what the element CONTAINS, not what it prints as.
		if (!value.every(isBoundedArrayElement)) return undefined;
		const total = value.reduce<number>(
			(sum, item) => sum + String(item ?? "").length,
			0,
		);
		// A copy, not the caller's array. Spans export on a batch delay, so a
		// mutation after sanitize() and before serialization would otherwise
		// change bytes that were already vetted.
		return total <= TRACE_ATTRIBUTE_VALUE_MAX_LENGTH
			? ([...value] as AttributeValue)
			: undefined;
	}
	return undefined;
}

/**
 * An array element that may leave the browser: null, or a primitive whose own
 * text is within the cap. Anything else — an object, a function, a nested
 * array — is not an attribute value, and its stringification says nothing
 * about what it holds.
 */
function isBoundedArrayElement(item: unknown): boolean {
	if (item === null || item === undefined) return true;
	if (typeof item === "number" || typeof item === "boolean") return true;
	return (
		typeof item === "string" && item.length <= TRACE_ATTRIBUTE_VALUE_MAX_LENGTH
	);
}
