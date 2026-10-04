// Which bytes of its source each piece of rendered markdown came from.
//
// An annotation layer maps a reader's selection back to the bytes of the version
// it was read from, and rendered text is not its source: markdown drops `**`, a
// list gains a bullet nobody wrote, a heading loses its `#`. So the renderer
// says which bytes it rendered rather than letting the reading layer count
// characters off the screen.
//
// The attributes and their meaning are the contract a composed module's
// annotation kit already reads (its `src/anchors/source-map.ts`), matched rather
// than reinvented:
//
//   data-source-start / data-source-end  UTF-8 offsets `[start, end)` into the
//                                        version's bytes
//   data-source-exact="false"            the element's text is NOT those bytes
//                                        verbatim, so a selection inside it
//                                        widens to the whole element
//   data-source-ignore                   chrome the renderer added (a copy
//                                        button): not text of the version
//
// Two things are easy to get wrong and are therefore done in one place here.
//
// **Bytes, not characters.** A markdown position is a JS string index; the
// contract counts UTF-8 bytes. `é` is one index and two bytes, an emoji two
// indices and four, so a document of plain ASCII passes a renderer that forgot
// the conversion and a French one does not. Every offset goes through
// `byteOffsets` before it reaches an attribute.
//
// **Exactness is measured, not assumed.** Rather than listing which constructs
// markdown rewrites, each element and each text run is compared with the source
// it claims, so a construct nobody thought of is marked inexact instead of
// lying. The reading layer compares too; this only saves it the work and covers
// the cases it cannot see.

import type { Element, ElementContent, Root, RootContent, Text } from "hast";

/** `data-source-start`: the first byte of the source an element was rendered from. */
export const SOURCE_START = "data-source-start";
/** `data-source-end`: one past the last byte (the range is half-open). */
export const SOURCE_END = "data-source-end";
/** `data-source-exact`: `"false"` when the text is not those bytes verbatim. */
export const SOURCE_EXACT = "data-source-exact";
/** `data-source-ignore`: chrome the renderer added; it maps to no source. */
export const SOURCE_IGNORE = "data-source-ignore";

/** A half-open byte range of the source, and whether the text is it verbatim. */
export interface SourceRange {
	start: number;
	end: number;
	/** False when markdown rewrote the text; the range then maps as a whole. */
	exact: boolean;
}

/** The `data-source-*` attributes a range renders as, spreadable onto an element. */
export interface SourceAttributes {
	"data-source-start"?: number;
	"data-source-end"?: number;
	"data-source-exact"?: string;
	"data-source-ignore"?: string;
}

/** The attributes for one range, or none at all when there is no range. */
export function sourceAttributes(range?: SourceRange): SourceAttributes {
	if (!range) return {};
	return {
		[SOURCE_START]: range.start,
		[SOURCE_END]: range.end,
		...(range.exact ? {} : { [SOURCE_EXACT]: "false" }),
	};
}

/**
 * The `data-source-*` attributes already on a set of props, forwarded as they
 * are. A renderer that intercepts an element (`<pre>`, `<td>`) must pass them
 * on: an element that drops them is an element an annotation cannot be written
 * on, and nothing fails at runtime to say so.
 */
export function sourceProps(props: object): SourceAttributes {
	const from = props as Record<string, unknown>;
	const out: SourceAttributes = {};
	const start = from[SOURCE_START];
	const end = from[SOURCE_END];
	if (typeof start === "number") out[SOURCE_START] = start;
	if (typeof end === "number") out[SOURCE_END] = end;
	if (from[SOURCE_EXACT] === "false") out[SOURCE_EXACT] = "false";
	if (from[SOURCE_IGNORE] !== undefined) out[SOURCE_IGNORE] = "";
	return out;
}

/** The range a set of props carries, for a renderer that passes it to a child. */
export function readSourceRange(props: object): SourceRange | undefined {
	const attributes = sourceProps(props);
	const start = attributes[SOURCE_START];
	const end = attributes[SOURCE_END];
	if (start === undefined || end === undefined) return undefined;
	return { start, end, exact: attributes[SOURCE_EXACT] !== "false" };
}

/**
 * Maps a JS string index in `source` to its UTF-8 byte offset, the space the
 * annotations locator counts in.
 *
 * A surrogate pair is one character of four bytes: its bytes are counted at the
 * high half, and the index between the halves — which names no byte — reads as
 * the character's end rather than splitting it. A lone surrogate counts as the
 * three bytes a UTF-8 encoder replaces it with, so the mapping agrees with
 * `TextEncoder` on text no encoder could round-trip.
 */
export function byteOffsets(source: string): (index: number) => number {
	const prefix = new Uint32Array(source.length + 1);
	let total = 0;
	for (let index = 0; index < source.length; index++) {
		prefix[index] = total;
		const code = source.charCodeAt(index);
		if (code < 0x80) total += 1;
		else if (code < 0x800) total += 2;
		else if (
			code >= 0xd800 &&
			code <= 0xdbff &&
			(source.charCodeAt(index + 1) & 0xfc00) === 0xdc00
		) {
			total += 4;
			prefix[++index] = total;
		} else total += 3;
	}
	prefix[source.length] = total;
	return (index) =>
		prefix[Math.min(Math.max(index, 0), source.length)] as number;
}

/** What the plugin needs to turn markdown positions into the version's bytes. */
export interface SourceOffsetsOptions {
	/** The markdown exactly as it was handed to the parser. */
	source: string;
	/**
	 * The byte offset at which `source` starts inside the version it is part of,
	 * so a body rendered without its front matter still names the version's
	 * bytes. Default 0.
	 */
	base?: number;
}

interface Marking {
	source: string;
	at: (index: number) => number;
	base: number;
}

function offsetsOf(node: {
	position?: { start?: { offset?: number }; end?: { offset?: number } };
}): [number, number] | undefined {
	const start = node.position?.start?.offset;
	const end = node.position?.end?.offset;
	if (typeof start !== "number" || typeof end !== "number") return undefined;
	return start <= end ? [start, end] : undefined;
}

function textOf(node: ElementContent | Root): string {
	if (node.type === "text") return node.value;
	if (node.type === "element" || node.type === "root") {
		let text = "";
		for (const child of node.children) text += textOf(child as ElementContent);
		return text;
	}
	return "";
}

function setRange(
	element: Element,
	start: number,
	end: number,
	exact: boolean,
	marking: Marking,
): void {
	element.properties ??= {};
	Object.assign(
		element.properties,
		sourceAttributes({
			start: marking.base + marking.at(start),
			end: marking.base + marking.at(end),
			exact,
		}),
	);
}

function mark(element: Element, marking: Marking): void {
	const range = offsetsOf(element);
	if (!range) return;
	setRange(
		element,
		range[0],
		range[1],
		textOf(element) === marking.source.slice(range[0], range[1]),
		marking,
	);
}

function wrap(text: Text, marking: Marking): Element | undefined {
	const range = offsetsOf(text);
	if (!range) return undefined;
	return {
		type: "element",
		tagName: "span",
		properties: {
			...sourceAttributes({
				start: marking.base + marking.at(range[0]),
				end: marking.base + marking.at(range[1]),
				exact: text.value === marking.source.slice(range[0], range[1]),
			}),
		},
		children: [text],
	};
}

/**
 * A fenced or indented code block. The block's own position covers the fence —
 * the backticks, the language, the closing line — and its text covers only the
 * body, which the parser hands over with no position of its own. So the `<code>`
 * carries the body's range, exact when the body is in the source verbatim (every
 * fence; an indented block had its indent stripped and is not), and the `<pre>`
 * carries the whole block, inexact. A reader therefore comments on the block as
 * a whole and selects inside it character for character.
 */
function markCode(pre: Element, marking: Marking): void {
	const code = pre.children.find(
		(child): child is Element =>
			child.type === "element" && child.tagName === "code",
	);
	const fence = offsetsOf(pre);
	if (code) {
		const span = offsetsOf(code) ?? fence;
		const body = textOf(code);
		const at = span ? marking.source.indexOf(body, span[0]) : -1;
		if (span && body !== "" && at >= 0 && at + body.length <= span[1]) {
			setRange(code, at, at + body.length, true, marking);
		} else if (span) setRange(code, span[0], span[1], false, marking);
	}
	if (fence) setRange(pre, fence[0], fence[1], false, marking);
}

function annotate(parent: Root | Element, marking: Marking): void {
	const out: ElementContent[] = [];
	for (const child of parent.children as ElementContent[]) {
		if (child.type === "element") {
			if (child.tagName === "pre") markCode(child, marking);
			else {
				annotate(child, marking);
				mark(child, marking);
			}
			out.push(child);
			continue;
		}
		out.push(child.type === "text" ? (wrap(child, marking) ?? child) : child);
	}
	parent.children = out as RootContent[] & ElementContent[];
}

/**
 * A rehype plugin that marks every element and every text run with the bytes it
 * was rendered from. Text the parser gave no position — a GFM footnote's
 * generated heading and its `↩` back-reference — is left unmarked and maps to
 * its nearest marked ancestor as a whole, which is what the contract says an
 * inexact element does.
 */
export function rehypeSourceOffsets(options: SourceOffsetsOptions) {
	const marking: Marking = {
		source: options.source,
		at: byteOffsets(options.source),
		base: options.base ?? 0,
	};
	return (tree: Root) => {
		annotate(tree, marking);
	};
}
