"use client";

// GitHub-flavoured markdown for content the host did not write: ingested
// documents and model output. The renderer builds React elements from a syntax
// tree, so nothing is ever parsed as HTML by the browser, and three rules hold
// whatever the source says:
//
//   1. Raw HTML is dropped, never rendered (`skipHtml`, and no rehype-raw).
//   2. A link is live only for an absolute http, https or mailto URL
//      (`safeLinkUrl`); anything else renders as its text. Live links open in a
//      new browsing context with `rel="noopener noreferrer"`. A caller that
//      knows where the content lives (`resolveLink`) may instead make a link
//      open something in the product; the kit never navigates by itself.
//   3. Images are OFF unless the caller opts in, because rendering one fetches
//      it: an untrusted document could otherwise make every reader's browser
//      call a URL of its choosing. Off, an image renders as its alt text.
//   4. A link the caller RESOLVES is held to the same allowlist as one the
//      content wrote, so `resolveLink` cannot turn `javascript:` into a link.
//   5. The only id that reaches the page is one the kit minted, under this
//      block's own `clobberPrefix` namespace (`useOwnId`). That is what makes a
//      GFM footnote's anchor work without letting a document write an id that
//      collides with, or shadows, the host's own — or another block's.

import type { Element, ElementContent } from "hast";
import {
	type ComponentPropsWithoutRef,
	createContext,
	type ReactNode,
	useContext,
	useId,
	useMemo,
} from "react";
import ReactMarkdown, {
	type Components,
	type ExtraProps,
} from "react-markdown";
import type { PluggableList } from "unified";
import { cn } from "../layout/cn.js";
import { CodeBlock } from "./code-block.js";
import { remarkGfmParse } from "./gfm.js";
import type { LinkResolver } from "./links.js";
import {
	protectReferences,
	REFERENCE_ELEMENT,
	remarkLineBreaks,
	remarkReferences,
} from "./references.js";
import {
	readSourceRange,
	rehypeSourceOffsets,
	SOURCE_IGNORE,
	type SourceAttributes,
	sourceProps,
} from "./source-offsets.js";
import { safeImageUrl, safeLinkUrl } from "./url.js";

/** A heading level, for mapping markdown's `#` onto the page's outline. */
export type HeadingLevel = 1 | 2 | 3 | 4 | 5 | 6;

export interface MarkdownProps {
	/** The markdown source. */
	children: string;
	/**
	 * The HTML level a markdown `#` renders as; `##` is one deeper, and so on,
	 * capped at 6. Default 3, so an answer or a snippet slots under the page's
	 * own title and section headings instead of competing with them. A full
	 * document preview typically passes 2.
	 */
	headingLevel?: HeadingLevel;
	/**
	 * Render images from absolute https URLs. Default false: the image renders
	 * as its alt text and nothing is fetched. Enable only for content whose
	 * image hosts you trust.
	 */
	allowImages?: boolean;
	/**
	 * Treat a single newline as a line break (chat and model output are written
	 * that way). Default false: CommonMark soft wraps.
	 */
	lineBreaks?: boolean;
	/**
	 * Resolve relative links against this URL: the content's own source (a
	 * document's location in its repository), supplied by the caller, never read
	 * from the content. The resolved link must still be http, https or mailto.
	 * Default none: a relative link is inert text.
	 */
	linkBase?: string;
	/**
	 * Decides what a link does, from its href exactly as the content wrote it —
	 * before `linkBase` and the allowlist apply. A document preview uses it to
	 * open another document of the same collection in place (see
	 * `resolveRelativeLink`), or to say a target cannot be shown. Returning
	 * `undefined` leaves the link to the kit's default rule.
	 */
	resolveLink?: LinkResolver;
	/**
	 * Numbered references (`[1]`, `[2]`) the caller renders itself, such as a
	 * citation marker. Only the listed numbers are references; any other `[n]`
	 * stays markdown. See references.ts for how a model's `[n](url)` and
	 * `[n]: url` forms are handled.
	 *
	 * Ignored under `sourceOffsets`: protecting a marker rewrites the source
	 * before it is parsed, which would move every offset after it. Citations are
	 * an answer's affordance, a byte range a stored document's.
	 */
	references?: MarkdownReferences;
	/**
	 * Mark what was rendered with the bytes it came from: `data-source-start` and
	 * `data-source-end` on every block and every text run, and
	 * `data-source-exact="false"` where markdown rewrote the text (a heading's
	 * `#`, emphasis, a link, a list bullet, a code fence). Plain runs are
	 * byte-exact, so an annotation layer maps a selection character for
	 * character inside them and to the whole element elsewhere.
	 *
	 * The offsets are UTF-8, half-open, and counted in the version's bytes — the
	 * contract of the annotations kit's text-range locator, not the kit's own
	 * invention. Default off: it adds a `<span>` around every text run.
	 */
	sourceOffsets?: boolean;
	/**
	 * The byte offset at which `children` starts inside the version it is part
	 * of. A document rendered without its front matter passes the front matter's
	 * byte length, so the offsets still name the version's bytes rather than the
	 * slice's. Default 0. Only read under `sourceOffsets`.
	 */
	sourceStart?: number;
	/** Show a copy button on each fenced code block. Default true. */
	copyable?: boolean;
	className?: string;
}

/** Numbered references rendered by the caller. */
export interface MarkdownReferences {
	/** The marker numbers that are references. */
	markers: Iterable<number>;
	/** Renders one marker in place of its `[n]`. */
	render: (marker: number) => ReactNode;
}

// The reference renderer reaches the (stable, module-level) marker component
// through context, so a new `render` closure on every streamed update does not
// remount the tree.
const ReferenceContext = createContext<((marker: number) => ReactNode) | null>(
	null,
);

function Reference({ marker }: { marker?: number | string }) {
	const render = useContext(ReferenceContext);
	const number = Number(marker);
	if (!render || !Number.isInteger(number)) return <>{`[${marker ?? ""}]`}</>;
	return <>{render(number)}</>;
}

// The type slot each markdown depth reads as. Depths past three share one:
// deep structure inside embedded content reads as emphasis, not a title.
const HEADING_SLOT = [
	"type-card-title",
	"type-card-title-sm",
	"type-emphasis",
] as const;

type HeadingTag = "h1" | "h2" | "h3" | "h4" | "h5" | "h6";

function headingFor(depth: number, base: HeadingLevel) {
	const level = Math.min(6, base + depth - 1);
	const Tag = `h${level}` as HeadingTag;
	const slot = HEADING_SLOT[Math.min(depth, HEADING_SLOT.length) - 1];
	return function Heading(props: ComponentPropsWithoutRef<"h1"> & ExtraProps) {
		const { id, className, children } = props;
		return (
			<Tag
				id={useOwnId(id)}
				className={cn("mt-4 mb-2 first:mt-0", slot, className)}
				{...sourceProps(props)}
			>
				{children}
			</Tag>
		);
	};
}

function textOf(node: ElementContent): string {
	if (node.type === "text") return node.value;
	if (node.type === "element") return node.children.map(textOf).join("");
	return "";
}

function codeLanguage(code: Element): string | undefined {
	const classes = code.properties.className;
	if (!Array.isArray(classes)) return undefined;
	for (const name of classes) {
		if (typeof name === "string" && name.startsWith("language-")) {
			return name.slice("language-".length);
		}
	}
	return undefined;
}

// The caller's link rules reach the (stable, module-level) link component
// through context, like the reference renderer.
// Whether a fenced block inside markdown offers a copy button. It rides a
// context rather than the components map because that map is cached per
// (headingLevel, allowImages) and its identity must stay stable across renders.
const CodeContext = createContext<{ copyable?: boolean }>({});

const LinkContext = createContext<{
	base?: string;
	resolve?: LinkResolver;
	/** This block's id namespace; see `useOwnId`. */
	anchorPrefix?: string;
}>({});

/**
 * An id is put on the page only when the kit minted it. Every rendered block
 * gets its own `clobberPrefix` (see `Markdown`), so a footnote's anchor target
 * is unique to the block and cannot collide with an id the host wrote or with
 * another block's. Anything else is dropped — `mdast-util-to-hast` hard-codes
 * an unprefixed `footnote-label` on the footnote heading, which would repeat on
 * every block that has footnotes, and the `aria-describedby` that was its only
 * consumer is dropped by the `a` component below.
 */
function useOwnId(id: unknown): string | undefined {
	const { anchorPrefix } = useContext(LinkContext);
	return typeof id === "string" &&
		anchorPrefix !== undefined &&
		id.startsWith(anchorPrefix)
		? id
		: undefined;
}

const LINK_CLASS =
	"text-primary underline underline-offset-2 hover:no-underline break-words";

function SafeLink({
	href,
	id,
	source,
	children,
}: {
	href: unknown;
	id?: unknown;
	/** The bytes this link was rendered from, when the caller asked for them. */
	source?: SourceAttributes;
	children?: ReactNode;
}) {
	const { base, resolve, anchorPrefix } = useContext(LinkContext);
	const ownId = useOwnId(id);
	// An anchor the kit generated for this block — a GFM footnote's marker and
	// its `↩` back-reference. It is not a link the content wrote, so it is
	// settled before the caller's resolver and before the URL allowlist: both
	// would refuse it (a bare `#fragment` is relative, so `safeLinkUrl` returns
	// undefined), which left the whole footnote feature rendering as inert text
	// you could neither follow nor get back from. The prefix is minted per
	// render tree, so content cannot forge one for another block or for the host.
	if (
		anchorPrefix !== undefined &&
		typeof href === "string" &&
		href.startsWith(`#${anchorPrefix}`)
	) {
		return (
			<a
				href={href}
				id={ownId}
				data-slot="content-link-anchor"
				className={LINK_CLASS}
				{...source}
			>
				{children}
			</a>
		);
	}
	const target =
		resolve && typeof href === "string" ? resolve(href) : undefined;
	if (target && "open" in target) {
		return (
			<button
				type="button"
				id={ownId}
				data-slot="content-link-internal"
				title={target.title}
				{...source}
				onClick={() => target.open()}
				className={cn(
					LINK_CLASS,
					"inline cursor-pointer border-0 bg-transparent p-0 text-left font-[inherit] [font-size:inherit]",
				)}
			>
				{children}
			</button>
		);
	}
	if (target && "unavailable" in target) {
		return (
			<span
				id={ownId}
				data-slot="content-link-unavailable"
				title={target.unavailable}
				className="underline decoration-dotted underline-offset-2"
				{...source}
			>
				{children}
				{/* The kit's words about the link, not the document's: inside a marked
				    element it would read as text of the source, so a selection on it
				    would map to the link's bytes. */}
				<span className="sr-only" {...{ [SOURCE_IGNORE]: "" }}>
					{" "}
					({target.unavailable})
				</span>
			</span>
		);
	}
	// A resolved href is the caller's answer to "where does this actually go",
	// and it is held to the same allowlist as one the content wrote: the caller
	// is trusted to know the destination, not to bypass the scheme rule.
	const resolved =
		target && "href" in target ? safeLinkUrl(target.href, base) : undefined;
	const safe = resolved ?? (target ? undefined : safeLinkUrl(href, base));
	if (!safe)
		return (
			<span id={ownId} data-slot="content-link-inert" {...source}>
				{children}
			</span>
		);
	const external = target && "href" in target ? target.external : true;
	return (
		<a
			href={safe}
			id={ownId}
			{...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
			className={LINK_CLASS}
			{...source}
		>
			{children}
		</a>
	);
}

function Fence(props: ComponentPropsWithoutRef<"pre"> & ExtraProps) {
	const { copyable } = useContext(CodeContext);
	const code = props.node?.children.find(
		(child): child is Element =>
			child.type === "element" && child.tagName === "code",
	);
	if (!code) return null;
	// The fence's text ends with the newline that closed it.
	const raw = textOf(code);
	const text = raw.replace(/\n$/, "");
	// `<code>` carries the body's range and `<pre>` the whole fence (see
	// markCode in source-offsets.ts). The newline stripped above is one byte of
	// that body: dropped from the text but not from the range, the reading layer
	// would find the text a byte short of its bytes and map the block as a whole
	// rather than character for character.
	const body = readSourceRange(code.properties ?? {});
	return (
		<CodeBlock
			code={text}
			language={codeLanguage(code)}
			copyable={copyable}
			className="my-2"
			source={readSourceRange(props)}
			bodySource={
				body && raw.endsWith("\n") ? { ...body, end: body.end - 1 } : body
			}
		/>
	);
}

// Named (and capitalised) so `useOwnId` is called from something the rules of
// hooks recognise as a component; the entry in the map below is the tag.
function ListItem(props: ComponentPropsWithoutRef<"li"> & ExtraProps) {
	const { id, className, children } = props;
	return (
		<li
			id={useOwnId(id)}
			className={cn(className === "task-list-item" && "flex items-start gap-2")}
			{...sourceProps(props)}
		>
			{children}
		</li>
	);
}

function buildComponents(
	headingLevel: HeadingLevel,
	allowImages: boolean,
): Components {
	return {
		// A custom element name: react-markdown resolves any tag in this map. It is
		// only ever produced by remarkReferences.
		...({ [REFERENCE_ELEMENT]: Reference } as unknown as Components),
		h1: headingFor(1, headingLevel),
		h2: headingFor(2, headingLevel),
		h3: headingFor(3, headingLevel),
		h4: headingFor(4, headingLevel),
		h5: headingFor(5, headingLevel),
		h6: headingFor(6, headingLevel),
		p: (props) => (
			<p className="my-2 first:mt-0 last:mb-0" {...sourceProps(props)}>
				{props.children}
			</p>
		),
		// `id` is forwarded only when the kit minted it: a footnote's marker is
		// the target of its own back-reference.
		a: (props) => (
			<SafeLink href={props.href} id={props.id} source={sourceProps(props)}>
				{props.children}
			</SafeLink>
		),
		img: (props) => {
			const { src, alt } = props;
			const safe = allowImages ? safeImageUrl(src) : undefined;
			if (!safe) {
				return alt ? (
					<span
						data-slot="content-image-inert"
						className="text-muted-foreground"
						{...sourceProps(props)}
					>
						[{alt}]
					</span>
				) : null;
			}
			return (
				<img
					src={safe}
					alt={alt ?? ""}
					loading="lazy"
					decoding="async"
					referrerPolicy="no-referrer"
					className="my-2 h-auto max-w-full rounded-md"
					{...sourceProps(props)}
				/>
			);
		},
		ul: (props) => (
			<ul
				className={cn(
					"my-2 list-disc space-y-1 pl-5",
					props.className === "contains-task-list" && "list-none pl-1",
				)}
				{...sourceProps(props)}
			>
				{props.children}
			</ul>
		),
		ol: (props) => (
			<ol
				start={props.start}
				className="my-2 list-decimal space-y-1 pl-5"
				{...sourceProps(props)}
			>
				{props.children}
			</ol>
		),
		li: ListItem,
		input: ({ type, checked }) =>
			type === "checkbox" ? (
				<input
					type="checkbox"
					checked={!!checked}
					disabled
					readOnly
					aria-label={checked ? "Done" : "Not done"}
					className="mt-1 accent-primary"
				/>
			) : null,
		blockquote: (props) => (
			<blockquote
				className="my-2 border-l-2 border-border pl-3 text-muted-foreground"
				{...sourceProps(props)}
			>
				{props.children}
			</blockquote>
		),
		hr: (props) => (
			<hr className="my-4 border-border" {...sourceProps(props)} />
		),
		table: (props) => (
			<div className="my-2 max-w-full overflow-x-auto">
				<table
					className="w-full border-collapse type-table"
					{...sourceProps(props)}
				>
					{props.children}
				</table>
			</div>
		),
		// GFM column alignment arrives as `style.textAlign`; nothing else does.
		th: (props) => (
			<th
				style={
					props.style?.textAlign
						? { textAlign: props.style.textAlign }
						: undefined
				}
				className="border-b border-border px-2 py-1 text-left type-table-head"
				{...sourceProps(props)}
			>
				{props.children}
			</th>
		),
		td: (props) => (
			<td
				style={
					props.style?.textAlign
						? { textAlign: props.style.textAlign }
						: undefined
				}
				className="border-b border-border px-2 py-1 align-top"
				{...sourceProps(props)}
			>
				{props.children}
			</td>
		),
		// Inline code. A fenced block is taken over whole by `pre` below, so this
		// only ever sees code inside a line.
		code: (props) => (
			<code
				className="rounded bg-muted px-1 py-0.5 font-mono [overflow-wrap:anywhere]"
				{...sourceProps(props)}
			>
				{props.children}
			</code>
		),
		pre: Fence,
	};
}

// Component identity must be stable across renders: a fresh `components` object
// would remount the whole tree on every update — every streamed token of an
// answer — and throw away each code block's loaded highlight. There are twelve
// combinations, so each is built once and kept.
const COMPONENTS = new Map<string, Components>();

function componentsFor(
	headingLevel: HeadingLevel,
	allowImages: boolean,
): Components {
	const key = `${headingLevel}:${allowImages}`;
	let components = COMPONENTS.get(key);
	if (!components) {
		components = buildComponents(headingLevel, allowImages);
		COMPONENTS.set(key, components);
	}
	return components;
}

// Images are decided here. An `a`'s href is passed through as written: the
// link component holds it to the caller's resolver and then the allowlist, and
// renders nothing live that fails both. Any other URL attribute keeps the
// allowlist here.
function transformUrl(allowImages: boolean) {
	return (url: string, key: string, node: Element) =>
		key === "src"
			? allowImages
				? safeImageUrl(url)
				: undefined
			: key === "href" && node.tagName === "a"
				? url
				: safeLinkUrl(url);
}

const URL_TRANSFORMS = {
	true: transformUrl(true),
	false: transformUrl(false),
} as const;
// Plugin lists are stable per combination, like the components.
const NO_REHYPE_PLUGINS: PluggableList = [];
const PLUGINS = {
	plain: [remarkGfmParse],
	breaks: [remarkGfmParse, remarkLineBreaks],
	references: [remarkGfmParse, remarkReferences],
	both: [remarkGfmParse, remarkLineBreaks, remarkReferences],
} as const;

/**
 * Render markdown (GFM: tables, task lists, strikethrough, autolinks, fenced
 * code) with the host's tokens and none of the source's HTML.
 */
export function Markdown({
	children,
	headingLevel = 3,
	allowImages = false,
	lineBreaks = false,
	linkBase,
	resolveLink,
	references,
	sourceOffsets = false,
	sourceStart = 0,
	copyable,
	className,
}: MarkdownProps) {
	// This block's own id namespace. `mdast-util-to-hast` derives a footnote's
	// id from the document (`[^a]` → `<prefix>fn-a`) under a `clobberPrefix` that
	// defaults to the well-known `user-content-`; minting one per render tree
	// keeps two blocks on a page from emitting the same id, and gives `useOwnId`
	// and `SafeLink` a way to tell an anchor the kit generated from one the
	// content wrote.
	const anchorPrefix = `content-${useId().replace(/[^a-zA-Z0-9]/g, "")}-`;
	const links = useMemo(
		() => ({ base: linkBase, resolve: resolveLink, anchorPrefix }),
		[linkBase, resolveLink, anchorPrefix],
	);
	const rehypeOptions = useMemo(
		() => ({ clobberPrefix: anchorPrefix }),
		[anchorPrefix],
	);
	const code = useMemo(() => ({ copyable }), [copyable]);
	const markers = references?.markers;
	// Offsets name the bytes of the source as given, so under `sourceOffsets` the
	// source is never rewritten: protecting a reference marker deletes a `[n]: url`
	// definition line, and every offset after it would be wrong by its length.
	const cited = useMemo(
		() => (markers && !sourceOffsets ? new Set(markers) : null),
		[markers, sourceOffsets],
	);
	const source = useMemo(
		() => (cited ? protectReferences(children, cited) : children),
		[children, cited],
	);
	const rehypePlugins = useMemo<PluggableList>(
		() =>
			sourceOffsets
				? [[rehypeSourceOffsets, { source, base: sourceStart }]]
				: NO_REHYPE_PLUGINS,
		[sourceOffsets, source, sourceStart],
	);
	const plugins = cited
		? lineBreaks
			? PLUGINS.both
			: PLUGINS.references
		: lineBreaks
			? PLUGINS.breaks
			: PLUGINS.plain;
	return (
		<ReferenceContext.Provider value={references?.render ?? null}>
			<CodeContext.Provider value={code}>
				<LinkContext.Provider value={links}>
					<div
						data-slot="content-markdown"
						className={cn("min-w-0 break-words", className)}
					>
						<ReactMarkdown
							remarkPlugins={[...plugins]}
							rehypePlugins={rehypePlugins}
							remarkRehypeOptions={rehypeOptions}
							skipHtml
							urlTransform={URL_TRANSFORMS[allowImages ? "true" : "false"]}
							components={componentsFor(headingLevel, allowImages)}
						>
							{source}
						</ReactMarkdown>
					</div>
				</LinkContext.Provider>
			</CodeContext.Provider>
		</ReferenceContext.Provider>
	);
}
