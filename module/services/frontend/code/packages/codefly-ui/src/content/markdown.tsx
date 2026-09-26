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

import type { Element, ElementContent } from "hast";
import {
	type ComponentPropsWithoutRef,
	createContext,
	type ReactNode,
	useContext,
	useMemo,
} from "react";
import ReactMarkdown, {
	type Components,
	type ExtraProps,
} from "react-markdown";
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
	 */
	references?: MarkdownReferences;
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
	return function Heading({
		id,
		className,
		children,
	}: ComponentPropsWithoutRef<"h1"> & ExtraProps) {
		return (
			<Tag id={id} className={cn("mt-4 mb-2 first:mt-0", slot, className)}>
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
const LinkContext = createContext<{
	base?: string;
	resolve?: LinkResolver;
}>({});

const LINK_CLASS =
	"text-primary underline underline-offset-2 hover:no-underline break-words";

function SafeLink({ href, children }: { href: unknown; children?: ReactNode }) {
	const { base, resolve } = useContext(LinkContext);
	const target =
		resolve && typeof href === "string" ? resolve(href) : undefined;
	if (target && "open" in target) {
		return (
			<button
				type="button"
				data-slot="content-link-internal"
				title={target.title}
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
				data-slot="content-link-unavailable"
				title={target.unavailable}
				className="underline decoration-dotted underline-offset-2"
			>
				{children}
				<span className="sr-only"> ({target.unavailable})</span>
			</span>
		);
	}
	const safe = safeLinkUrl(href, base);
	if (!safe) return <span data-slot="content-link-inert">{children}</span>;
	return (
		<a
			href={safe}
			target="_blank"
			rel="noopener noreferrer"
			className={LINK_CLASS}
		>
			{children}
		</a>
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
		p: ({ children }) => (
			<p className="my-2 first:mt-0 last:mb-0">{children}</p>
		),
		a: ({ href, children }) => <SafeLink href={href}>{children}</SafeLink>,
		img: ({ src, alt }) => {
			const safe = allowImages ? safeImageUrl(src) : undefined;
			if (!safe) {
				return alt ? (
					<span
						data-slot="content-image-inert"
						className="text-muted-foreground"
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
				/>
			);
		},
		ul: ({ className, children }) => (
			<ul
				className={cn(
					"my-2 list-disc space-y-1 pl-5",
					className === "contains-task-list" && "list-none pl-1",
				)}
			>
				{children}
			</ul>
		),
		ol: ({ start, children }) => (
			<ol start={start} className="my-2 list-decimal space-y-1 pl-5">
				{children}
			</ol>
		),
		li: ({ id, className, children }) => (
			<li
				id={id}
				className={cn(
					className === "task-list-item" && "flex items-start gap-2",
				)}
			>
				{children}
			</li>
		),
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
		blockquote: ({ children }) => (
			<blockquote className="my-2 border-l-2 border-border pl-3 text-muted-foreground">
				{children}
			</blockquote>
		),
		hr: () => <hr className="my-4 border-border" />,
		table: ({ children }) => (
			<div className="my-2 max-w-full overflow-x-auto">
				<table className="w-full border-collapse type-table">{children}</table>
			</div>
		),
		// GFM column alignment arrives as `style.textAlign`; nothing else does.
		th: ({ style, children }) => (
			<th
				style={style?.textAlign ? { textAlign: style.textAlign } : undefined}
				className="border-b border-border px-2 py-1 text-left type-table-head"
			>
				{children}
			</th>
		),
		td: ({ style, children }) => (
			<td
				style={style?.textAlign ? { textAlign: style.textAlign } : undefined}
				className="border-b border-border px-2 py-1 align-top"
			>
				{children}
			</td>
		),
		// Inline code. A fenced block is taken over whole by `pre` below, so this
		// only ever sees code inside a line.
		code: ({ children }) => (
			<code className="rounded bg-muted px-1 py-0.5 font-mono [overflow-wrap:anywhere]">
				{children}
			</code>
		),
		pre: ({ node }) => {
			const code = node?.children.find(
				(child): child is Element =>
					child.type === "element" && child.tagName === "code",
			);
			if (!code) return null;
			// The fence's text ends with the newline that closed it.
			const text = textOf(code).replace(/\n$/, "");
			return (
				<CodeBlock code={text} language={codeLanguage(code)} className="my-2" />
			);
		},
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
	className,
}: MarkdownProps) {
	const links = useMemo(
		() => ({ base: linkBase, resolve: resolveLink }),
		[linkBase, resolveLink],
	);
	const markers = references?.markers;
	const cited = useMemo(() => (markers ? new Set(markers) : null), [markers]);
	const source = useMemo(
		() => (cited ? protectReferences(children, cited) : children),
		[children, cited],
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
			<LinkContext.Provider value={links}>
				<div
					data-slot="content-markdown"
					className={cn("min-w-0 break-words", className)}
				>
					<ReactMarkdown
						remarkPlugins={[...plugins]}
						skipHtml
						urlTransform={URL_TRANSFORMS[allowImages ? "true" : "false"]}
						components={componentsFor(headingLevel, allowImages)}
					>
						{source}
					</ReactMarkdown>
				</div>
			</LinkContext.Provider>
		</ReferenceContext.Provider>
	);
}
