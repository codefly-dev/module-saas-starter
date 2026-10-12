/**
 * W3C Trace Context (https://www.w3.org/TR/trace-context/) carried from a
 * browser request to an upstream one.
 *
 * Both headers are caller-supplied, so neither is copied as received: each is
 * parsed strictly and the upstream gets a re-serialization of what parsed.
 * Anything that does not parse is dropped, never repaired, and `tracestate`
 * only travels with a `traceparent` that did.
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
const TRACESTATE_LARGE_MEMBER_BYTES = 128;
const TRACESTATE_KEY =
	/^(?:[a-z][a-z0-9_\-*/]{0,255}|[a-z0-9][a-z0-9_\-*/]{0,240}@[a-z][a-z0-9_\-*/]{0,13})$/;
// value = 0*255(chr) nblk-chr; chr = %x20 / nblk-chr;
// nblk-chr = %x21-2B / %x2D-3C / %x3E-7E (printable ASCII but "," and "=").
const TRACESTATE_VALUE =
	/^[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$/;

/**
 * Parse one `traceparent` header value and return its canonical form, or null.
 *
 * Version 00 must be exactly the 55-character prefix. A later version (other
 * than the reserved ff) is parsed forward-compatibly: after the flags comes
 * either the end of the value or a "-" followed by fields that are not
 * interpreted. Only the 00-shaped prefix is understood, so a later version is
 * re-emitted as version 00 carrying just the sampled flag, never advertising a
 * version or flag bits this parser does not implement. Two header values,
 * which a Headers object reports joined by ", ", never parse.
 */
export function parseTraceparent(raw: string | null): string | null {
	if (raw === null || raw.length < TRACEPARENT_PREFIX_LENGTH) return null;
	const prefix = raw.slice(0, TRACEPARENT_PREFIX_LENGTH);
	const match = TRACEPARENT_PREFIX.exec(prefix);
	if (!match) return null;
	const [, version, traceId, parentId, flags] = match;
	if (version === INVALID_VERSION) return null;
	if (traceId === ZERO_TRACE_ID || parentId === ZERO_PARENT_ID) return null;
	const rest = raw.slice(TRACEPARENT_PREFIX_LENGTH);
	if (version === CURRENT_VERSION) {
		return rest === "" ? `${version}-${traceId}-${parentId}-${flags}` : null;
	}
	// A future version's extra fields start with "-" and are visible ASCII;
	// a "," would mean a second header value rode along.
	if (rest !== "" && !FUTURE_VERSION_TAIL.test(rest)) return null;
	const sampled = Number.parseInt(flags, 16) & SAMPLED_FLAG;
	const canonicalFlags = sampled.toString(16).padStart(FLAGS_HEX_LENGTH, "0");
	return `${CURRENT_VERSION}-${traceId}-${parentId}-${canonicalFlags}`;
}

/**
 * Parse a `tracestate` header (several header values arrive joined by ",") and
 * return its canonical form, or null when nothing should be forwarded.
 *
 * Any malformed member, a duplicate key, or more than 32 members drops the
 * whole list. A list over 512 bytes is trimmed the way the specification
 * prescribes: members over 128 bytes go first, then members from the right.
 */
export function parseTracestate(raw: string | null): string | null {
	if (raw === null) return null;
	const members: string[] = [];
	const keys = new Set<string>();
	for (const field of raw.split(",")) {
		const member = field.replace(/^[ \t]+|[ \t]+$/g, "");
		if (member === "") continue;
		const eq = member.indexOf("=");
		if (eq <= 0) return null;
		const key = member.slice(0, eq);
		const value = member.slice(eq + 1);
		if (!TRACESTATE_KEY.test(key) || !TRACESTATE_VALUE.test(value)) {
			return null;
		}
		if (keys.has(key)) return null;
		keys.add(key);
		members.push(`${key}=${value}`);
	}
	if (members.length === 0 || members.length > TRACESTATE_MAX_MEMBERS) {
		return null;
	}

	// Members are ASCII-only (enforced above), so length is byte length.
	const size = (list: string[]) =>
		list.reduce((total, member) => total + member.length, 0) +
		Math.max(list.length - 1, 0);
	let kept = members;
	while (size(kept) > TRACESTATE_MAX_BYTES) {
		const large = kept.findLastIndex(
			(member) => member.length > TRACESTATE_LARGE_MEMBER_BYTES,
		);
		if (large === -1) break;
		kept = kept.filter((_, i) => i !== large);
	}
	while (size(kept) > TRACESTATE_MAX_BYTES) {
		kept = kept.slice(0, -1);
	}
	return kept.length > 0 ? kept.join(",") : null;
}

/**
 * Copy the caller's validated trace context onto upstream headers. Only
 * `traceparent` and `tracestate` are considered; `baggage` and every other
 * header stay behind.
 */
export function forwardTraceContext(from: Headers, to: Headers): void {
	const traceparent = parseTraceparent(from.get("traceparent"));
	if (traceparent === null) return;
	to.set("traceparent", traceparent);
	const tracestate = parseTracestate(from.get("tracestate"));
	if (tracestate !== null) to.set("tracestate", tracestate);
}
