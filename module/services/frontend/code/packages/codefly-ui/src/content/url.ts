// The one place the content tier decides whether a URL from untrusted text may
// become a live link or an image source. Content reaching these components comes
// from ingested documents and model output, so the rule is an allowlist of
// schemes, never a denylist of known-bad ones: `javascript:`, `data:`,
// `vbscript:`, `file:` and every scheme not yet invented all fall out the same way.

const LINK_PROTOCOLS: ReadonlySet<string> = new Set([
	"http:",
	"https:",
	"mailto:",
]);

// An image is fetched the moment it renders, so it is held to less than a link:
// https only (no mixed content, no plaintext beacon), and only when the caller
// opted in to images at all.
const IMAGE_PROTOCOLS: ReadonlySet<string> = new Set(["https:"]);

function parseAbsolute(url: unknown, base?: string): URL | undefined {
	if (typeof url !== "string") return undefined;
	const trimmed = url.trim();
	if (trimmed === "") return undefined;
	try {
		// Without a base a relative URL throws and is refused: untrusted content
		// has no business pointing at a route of the page that renders it. A base
		// is the CALLER's (a document's own source location), never the content's,
		// and the result still has to pass the scheme allowlist.
		return new URL(trimmed, base || undefined);
	} catch {
		return undefined;
	}
}

/**
 * The normalized href for a link in untrusted content, or `undefined` when the
 * link must not be live: a scheme other than http, https or mailto, a relative
 * URL, or credentials in the authority (`https://bank.example@evil.example`
 * reads as one host and goes to another). With `base`, a relative URL is
 * resolved against it first — the caller's trusted location for the content,
 * such as the document's source — and then held to the same rules.
 */
export function safeLinkUrl(url: unknown, base?: string): string | undefined {
	const parsed = parseAbsolute(url, base);
	if (!parsed || !LINK_PROTOCOLS.has(parsed.protocol)) return undefined;
	if (parsed.username !== "" || parsed.password !== "") return undefined;
	return parsed.href;
}

/** The normalized source for an opted-in image, or `undefined` (https only). */
export function safeImageUrl(url: unknown): string | undefined {
	const parsed = parseAbsolute(url);
	if (!parsed || !IMAGE_PROTOCOLS.has(parsed.protocol)) return undefined;
	if (parsed.username !== "" || parsed.password !== "") return undefined;
	return parsed.href;
}
