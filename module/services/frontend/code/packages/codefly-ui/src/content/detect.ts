// Format detection for `<Content format="auto">`. Deliberately conservative:
// JSON is recognised only when it parses, and markdown only on a marker that
// plain prose rarely carries, so an ordinary sentence stays text.

/** The formats `<Content>` renders. `auto` resolves to one of the others. */
export type ContentFormat = "auto" | "markdown" | "json" | "code" | "text";

/** A concrete format, after `auto` has been resolved. */
export type ResolvedContentFormat = Exclude<ContentFormat, "auto">;

/** The outcome of reading a value as JSON. */
export type JsonReading =
	| { readonly ok: true; readonly value: unknown }
	| { readonly ok: false };

/**
 * Read a value as JSON. A non-string is already structured data; a string is
 * JSON only when it is an object or array literal that parses, so a bare word,
 * number or quoted string stays text rather than turning into a one-leaf tree.
 */
export function readJson(value: unknown): JsonReading {
	if (typeof value !== "string") {
		return value === undefined ? { ok: false } : { ok: true, value };
	}
	const trimmed = value.trim();
	const first = trimmed[0];
	const last = trimmed[trimmed.length - 1];
	if (!((first === "{" && last === "}") || (first === "[" && last === "]"))) {
		return { ok: false };
	}
	try {
		return { ok: true, value: JSON.parse(trimmed) as unknown };
	} catch {
		return { ok: false };
	}
}

// Each pattern is one markdown construct that prose does not produce by
// accident. Multiline so `^` anchors at every line start.
const MARKDOWN_MARKERS: readonly RegExp[] = [
	/^ {0,3}#{1,6}[ \t]+\S/m, // ATX heading
	/^ {0,3}(?:```|~~~)/m, // fenced code
	/^ {0,3}>[ \t]?\S/m, // blockquote
	/^ {0,3}[-*+][ \t]+\S/m, // bullet list item
	/^ {0,3}\d{1,9}[.)][ \t]+\S/m, // ordered list item
	/^ {0,3}\|?[ \t]*:?-{3,}:?[ \t]*\|/m, // table delimiter row
	/\[[^\]\n]+\]\([^)\s]+\)/, // inline link
	/(?:\*\*|__)[^*_\n]+(?:\*\*|__)/, // strong
	/`[^`\n]+`/, // inline code
];

/** Whether a string carries a markdown construct. */
export function looksLikeMarkdown(value: string): boolean {
	return MARKDOWN_MARKERS.some((marker) => marker.test(value));
}

/**
 * Resolve `auto`: a parseable object/array (or any non-string value) is json,
 * a string carrying a markdown marker is markdown, anything else is text.
 */
export function detectFormat(value: unknown): ResolvedContentFormat {
	if (value === undefined || value === null) return "text";
	if (typeof value !== "string") return "json";
	if (readJson(value).ok) return "json";
	if (looksLikeMarkdown(value)) return "markdown";
	return "text";
}
