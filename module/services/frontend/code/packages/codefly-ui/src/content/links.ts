// Links between documents. A markdown document that links another by a
// relative path (`../guide/setup.md`, `setup.md#install`) names a place in its
// own repository, not a URL: only the caller knows where that document is shown
// in the product, or whether the viewer may read it at all. So the kit resolves
// the path, and the caller decides what the link does.

/**
 * What a link in rendered content does, as the caller decides it.
 *
 * - `{ open }`: an in-product link. It renders as a link-styled button that
 *   calls `open`, and navigates nowhere by itself.
 * - `{ unavailable }`: the link's text, not live, with the reason as its title.
 *   For a target the caller cannot show: not in the viewer's readable set, or
 *   not there at all. The reason must not say which.
 * - `{ href, external }`: where the link actually goes, which only the caller
 *   knows — a corpus page's `../decisions/x.md#why` is a path in a repository,
 *   not a URL. `external` opens it in a new browsing context with
 *   `rel="noopener noreferrer"`; otherwise it opens in place. The href is held
 *   to the SAME allowlist as one the content wrote (`safeLinkUrl`, resolved
 *   against `linkBase` when there is one), so a resolver cannot make
 *   `javascript:` into a link: give an absolute http, https or mailto URL, or
 *   return `{ open }` to navigate inside the product without one.
 * - `undefined`: the kit's own rule applies (an absolute http, https or mailto
 *   URL is live in a new tab; `linkBase` resolves a relative one first).
 */
export type LinkTarget =
	| { open: () => void; title?: string }
	| { unavailable: string }
	| { href: string; external: boolean }
	| undefined;

/** Decides, from a link's href exactly as written, what the link does. */
export type LinkResolver = (href: string) => LinkTarget;

/** A relative link resolved against the path of the document that holds it. */
export interface RelativeLink {
	/**
	 * The target's path from the repository root, `/`-separated with no leading
	 * slash, `.` and `..` applied and percent-escapes decoded. The document's own
	 * path for a fragment-only link.
	 */
	path: string;
	/** The fragment without its `#`, decoded; "" when there is none. */
	fragment: string;
	/** True when the link named a folder (it ended in `/`), not a file. */
	directory: boolean;
}

const SCHEME = /^[a-z][a-z0-9+.-]*:/i;

function decode(value: string): string | null {
	try {
		return decodeURIComponent(value);
	} catch {
		return null;
	}
}

/**
 * Resolves `href`, as written in the document at `documentPath`, to a path in
 * the same repository — the way a repository host resolves a link between two
 * of its files. A path starting with `/` is from the repository root.
 *
 * Returns null for anything that is not a link inside the repository: an
 * absolute URL (any scheme), a protocol-relative one (`//host/…`), an empty
 * href, a path that climbs above the root, or an undecodable escape. Those keep
 * the kit's default handling.
 */
export function resolveRelativeLink(
	documentPath: string,
	href: string,
): RelativeLink | null {
	const written = href.trim();
	if (!written || SCHEME.test(written) || written.startsWith("//")) return null;
	const hash = written.indexOf("#");
	const beforeHash = hash < 0 ? written : written.slice(0, hash);
	const fragment = decode(hash < 0 ? "" : written.slice(hash + 1));
	if (fragment === null) return null;
	const query = beforeHash.indexOf("?");
	const target = query < 0 ? beforeHash : beforeHash.slice(0, query);
	const own = documentPath.replace(/^\/+/, "");
	if (target === "") {
		// `#section` or `?plain=1`: this document.
		return own ? { path: own, fragment, directory: false } : null;
	}
	const fromRoot = target.startsWith("/");
	const base = fromRoot ? [] : own.split("/").slice(0, -1);
	const segments = [...base];
	for (const raw of target.split("/")) {
		const segment = decode(raw);
		if (segment === null) return null;
		// The split above happens before this decode, so a percent-escaped
		// separator would arrive as one segment carrying a `/` of its own: the
		// climb check below would never see the `..` in `..%2f..%2f`, and
		// `join("/")` would put the separators back, handing the caller a path
		// that walks out of the repository the plain spelling is refused for. No
		// name in a repository holds a literal `/`, so an escape that decodes to
		// one is refused rather than resolved.
		if (segment.includes("/")) return null;
		if (segment === "" || segment === ".") continue;
		if (segment === "..") {
			if (segments.length === 0) return null;
			segments.pop();
			continue;
		}
		segments.push(segment);
	}
	const directory = target.endsWith("/") || /(^|\/)\.\.?$/.test(target);
	if (segments.length === 0 && !directory) return null;
	return { path: segments.join("/"), fragment, directory };
}

/**
 * The anchor a repository host gives a heading: lower-cased, punctuation
 * dropped, spaces as hyphens (`## Set up: step 2` → `set-up-step-2`). It lets
 * a caller find the heading a `#fragment` names in content the kit rendered,
 * without giving a heading an id derived from its own text — one taken from
 * untrusted content could collide with, or shadow, the host's own. (The
 * renderer does emit ids for GFM footnote anchors, but only ones it minted
 * under a prefix unique to each rendered block; see `useOwnId` in markdown.tsx.)
 */
export function headingSlug(text: string): string {
	return text
		.trim()
		.toLowerCase()
		.replace(/[^\p{L}\p{M}\p{N}\p{Pc} -]/gu, "")
		.replace(/ /g, "-");
}

/**
 * The first heading in `container` whose slug is `fragment`, or null. For
 * scrolling a `#fragment` into view after the caller opened its document.
 */
export function findHeading(
	container: ParentNode,
	fragment: string,
): HTMLElement | null {
	const wanted = headingSlug(fragment);
	if (!wanted) return null;
	for (const heading of container.querySelectorAll<HTMLElement>(
		"h1, h2, h3, h4, h5, h6",
	)) {
		if (headingSlug(heading.textContent ?? "") === wanted) return heading;
	}
	return null;
}
