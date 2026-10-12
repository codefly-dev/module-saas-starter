/**
 * W3C Trace Context (https://www.w3.org/TR/trace-context/) carried from a
 * caller's request to an upstream one.
 *
 * Both headers are caller-supplied, so neither is copied as received: each is
 * parsed strictly and the upstream gets a re-serialization of what parsed.
 * Anything that does not parse is dropped, never repaired, and `tracestate`
 * only travels with a `traceparent` that did.
 *
 * One parser serves every boundary in this service — the solution proxy and
 * the plugin BFF — because a boundary that answers the same header differently
 * from its sibling is a bug nobody can see from either side.
 *
 * The sampled flag is an *instruction* to the upstream's sampler rather than
 * inert metadata, so {@link TraceForwardingPolicy} decides whether the caller
 * is allowed to give it. See `module/docs/frontend-plugin-request-correlation.md`,
 * which this module implements.
 */

const TRACEPARENT_PREFIX_LENGTH = 55;
const TRACEPARENT_PREFIX =
	/^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$/;
const INVALID_VERSION = "ff";
const CURRENT_VERSION = "00";
const ZERO_TRACE_ID = "0".repeat(32);
const ZERO_PARENT_ID = "0".repeat(16);
const FUTURE_VERSION_TAIL = /^-[\x21-\x2b\x2d-\x7e]*$/;
const SAMPLED_FLAG = 0x01;
const FLAGS_HEX_LENGTH = 2;

const TRACESTATE_MAX_MEMBERS = 32;
const TRACESTATE_MAX_BYTES = 512;
const TRACESTATE_LARGE_VALUE_BYTES = 128;
const TRACESTATE_KEY =
	/^(?:[a-z][a-z0-9_\-*/]{0,255}|[a-z0-9][a-z0-9_\-*/]{0,240}@[a-z][a-z0-9_\-*/]{0,13})$/;
// value = 0*255(chr) nblk-chr; chr = %x20 / nblk-chr;
// nblk-chr = %x21-2B / %x2D-3C / %x3E-7E (printable ASCII but "," and "=").
const TRACESTATE_VALUE =
	/^[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$/;

/** A `traceparent` broken into the fields this parser understands. */
export interface TraceParent {
	/** The caller's version, kept as sent. */
	readonly version: string;
	readonly traceId: string;
	readonly parentId: string;
	/** trace-flags exactly as the caller sent them, every bit included. */
	readonly flags: string;
	/**
	 * A later version's uninterpreted fields, leading "-" and all; "" on
	 * version 00, which has none.
	 */
	readonly tail: string;
}

/** What a caller is allowed to decide about the upstream's tracing. */
export interface TraceForwardingPolicy {
	/**
	 * Whether this caller may set the upstream's sampling decision.
	 *
	 * Pass `true` only for a caller that presented a credential the upstream
	 * will verify. For anyone else the trace is still linked — the trace id and
	 * parent id travel, so spans join — but the sampled bit is cleared and
	 * `tracestate` stays behind: several vendors carry their own sampling state
	 * in `tracestate` (OpenTelemetry's `ot=` entry among them), so forwarding it
	 * would hand back the decision clearing the bit just took away.
	 */
	readonly honorSampledFlag: boolean;
}

/**
 * Parse one `traceparent` header value into its fields, or return null.
 *
 * Version 00 must be exactly the 55-character prefix and nothing else. A later
 * version (other than the reserved ff) is parsed forward-compatibly: after the
 * flags comes either the end of the value or a "-" followed by fields this
 * parser does not interpret and keeps in {@link TraceParent.tail}. Two header
 * values, which a Headers object reports joined by ", ", never parse.
 */
export function parseTraceparent(raw: string | null): TraceParent | null {
	if (raw === null || raw.length < TRACEPARENT_PREFIX_LENGTH) return null;
	const prefix = raw.slice(0, TRACEPARENT_PREFIX_LENGTH);
	const match = TRACEPARENT_PREFIX.exec(prefix);
	if (!match) return null;
	const [, version, traceId, parentId, flags] = match;
	if (version === INVALID_VERSION) return null;
	if (traceId === ZERO_TRACE_ID || parentId === ZERO_PARENT_ID) return null;
	const tail = raw.slice(TRACEPARENT_PREFIX_LENGTH);
	if (version === CURRENT_VERSION) {
		if (tail !== "") return null;
	} else if (tail !== "" && !FUTURE_VERSION_TAIL.test(tail)) {
		// A future version's extra fields start with "-" and are visible ASCII;
		// a "," would mean a second header value rode along.
		return null;
	}
	return { version, traceId, parentId, flags, tail };
}

/**
 * Serialize a parsed `traceparent` for the upstream.
 *
 * The caller's version, flag bits and uninterpreted later-version fields all
 * travel unchanged. This is a relay, not a tracer: it starts no span and
 * assigns no new parent id, so it has nothing of its own to advertise and no
 * reason to normalise — rewriting a later version as 00 would make this host
 * assert a context nobody produced and destroy flag bits (the Level 2 `random`
 * bit among them) that a newer upstream does understand. Re-serializing from
 * the parsed fields rather than copying the raw value is what guarantees no
 * unparsed byte reaches the upstream.
 *
 * The one field a policy may change is the sampled bit, cleared in place by
 * `honorSampledFlag: false`. Its position is fixed in every version, so
 * clearing one understood bit stays safe even in a version this parser cannot
 * otherwise read.
 */
export function serializeTraceparent(
	parsed: TraceParent,
	{ honorSampledFlag }: TraceForwardingPolicy,
): string {
	const flags = honorSampledFlag
		? parsed.flags
		: (Number.parseInt(parsed.flags, 16) & ~SAMPLED_FLAG)
				.toString(16)
				.padStart(FLAGS_HEX_LENGTH, "0");
	return `${parsed.version}-${parsed.traceId}-${parsed.parentId}-${flags}${parsed.tail}`;
}

interface TracestateMember {
	readonly serialized: string;
	/** The value's own length, which is what the truncation rule bounds. */
	readonly valueLength: number;
}

/**
 * Parse a `tracestate` header (several header values arrive joined by ",") and
 * return its canonical form, or null when nothing should be forwarded.
 *
 * Three rules, one per class of input, so the answer is predictable without
 * running it:
 *
 * 1. An **empty list member** is skipped. The specification allows them and
 *    asks senders to avoid them; it does not make the list unreadable.
 * 2. A member that **does not parse**, or that repeats a key already seen,
 *    discards the **whole list**. A list with a syntax error has no remainder
 *    that can be read safely, and a repeated key has no authoritative value.
 * 3. A list past **either** bound — 32 members or 512 bytes — is **truncated**
 *    from the right, members whose value exceeds 128 characters first. Both
 *    bounds come from the same section of the specification and get the same
 *    answer; discarding for one and truncating for the other would drop every
 *    other vendor's state over a single over-long hop.
 */
export function parseTracestate(raw: string | null): string | null {
	if (raw === null) return null;
	const members: TracestateMember[] = [];
	const keys = new Set<string>();
	for (const field of raw.split(",")) {
		const member = field.replace(/^[ \t]+|[ \t]+$/g, "");
		// Rule 1.
		if (member === "") continue;
		const eq = member.indexOf("=");
		// Rule 2, for the rest of this block.
		if (eq <= 0) return null;
		const key = member.slice(0, eq);
		const value = member.slice(eq + 1);
		if (!TRACESTATE_KEY.test(key) || !TRACESTATE_VALUE.test(value)) {
			return null;
		}
		if (keys.has(key)) return null;
		keys.add(key);
		members.push({
			serialized: `${key}=${value}`,
			valueLength: value.length,
		});
	}
	if (members.length === 0) return null;

	// Rule 3. Members are ASCII-only (enforced above), so length is byte length.
	const size = (list: readonly TracestateMember[]) =>
		list.reduce((total, member) => total + member.serialized.length, 0) +
		Math.max(list.length - 1, 0);
	let kept = members.slice(0, TRACESTATE_MAX_MEMBERS);
	while (size(kept) > TRACESTATE_MAX_BYTES) {
		const large = kept.findLastIndex(
			(member) => member.valueLength > TRACESTATE_LARGE_VALUE_BYTES,
		);
		if (large === -1) break;
		kept = kept.filter((_, i) => i !== large);
	}
	while (size(kept) > TRACESTATE_MAX_BYTES) {
		kept = kept.slice(0, -1);
	}
	return kept.length > 0
		? kept.map((member) => member.serialized).join(",")
		: null;
}

/**
 * Copy the caller's validated trace context onto upstream headers under
 * `policy`. Only `traceparent` and `tracestate` are considered; `baggage` and
 * every other header stay behind.
 */
export function forwardTraceContext(
	from: Headers,
	to: Headers,
	policy: TraceForwardingPolicy,
): void {
	const parsed = parseTraceparent(from.get("traceparent"));
	if (parsed === null) return;
	to.set("traceparent", serializeTraceparent(parsed, policy));
	// Per TraceForwardingPolicy: a caller who may not set the sampling decision
	// may not carry the vendor state that re-states it either.
	if (!policy.honorSampledFlag) return;
	const tracestate = parseTracestate(from.get("tracestate"));
	if (tracestate !== null) to.set("tracestate", tracestate);
}
