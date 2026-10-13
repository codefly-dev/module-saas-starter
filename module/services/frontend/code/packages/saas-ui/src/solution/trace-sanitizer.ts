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
	 * Span (and event) names the caller vouches for. Given, every other name
	 * that is not a bare HTTP method becomes the placeholder. Omitted, names
	 * pass once their query is cut and their length is within the cap.
	 */
	readonly allowedSpanNames?: readonly string[];
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
	private readonly allowedNames: ReadonlySet<string> | null;
	// One sanitized resource per source resource, so a batch still groups
	// under a single resource when it is serialized.
	private readonly resources = new WeakMap<Resource, Resource>();

	constructor(
		private readonly inner: SpanExporter,
		policy: TraceSanitizerPolicy,
	) {
		this.allowedKeys = new Set(
			[...TRACE_STANDARD_ATTRIBUTE_KEYS, ...policy.allowedAttributeKeys].filter(
				(key) => !isDeniedAttributeKey(key),
			),
		);
		this.allowedNames = policy.allowedSpanNames
			? new Set(policy.allowedSpanNames)
			: null;
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
		if (this.allowedNames) {
			return this.allowedNames.has(name) ? name : TRACE_SPAN_NAME_PLACEHOLDER;
		}
		const cut = stripQuery(name);
		return cut && cut.length <= TRACE_ATTRIBUTE_VALUE_MAX_LENGTH
			? cut
			: TRACE_SPAN_NAME_PLACEHOLDER;
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
		const total = value.reduce<number>(
			(sum, item) => sum + String(item ?? "").length,
			0,
		);
		return total <= TRACE_ATTRIBUTE_VALUE_MAX_LENGTH ? value : undefined;
	}
	return undefined;
}
