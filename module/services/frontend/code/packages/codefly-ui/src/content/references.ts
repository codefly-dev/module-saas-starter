// Numbered references inside markdown: a grounded answer cites its sources as
// `[1]`, `[2]`, and the caller renders each marker as whatever it links to (a
// popover, a jump to the source). A cited `[n]` is a reference before it is
// markdown. CommonMark would read `[1](url)` as a link and `[1]` as a reference
// to a `[1]: url` definition, both of which a model routinely emits, so:
//
//   - before parsing, each cited `[n]` becomes a private-use sentinel, a cited
//     marker's `[n]: url` definition line is dropped, and the `(url)` of a cited
//     `[n](url)` is dropped (the reference is the source, not the model's URL);
//     a sentinel is reversible anywhere, but a DELETION is not, so the two
//     rules that delete are held to the code regions the parser reports and
//     never touch a fence or a code span (see `codeRanges`);
//   - after parsing, sentinels in ordinary text become a `content-reference`
//     element the renderer maps to the caller's component, and everywhere else
//     (code, link text, URLs, titles, alt text) they turn back into a literal
//     `[n]`, so a `[2]` in a code span stays literal and a marker never nests
//     inside a link.
//
// A bracketed number that is not a reference is untouched.

import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";

/** The hast element name a reference marker lowers to. */
export const REFERENCE_ELEMENT = "content-reference";

const MARKER = /\[(\d+)\]/g;
const OPEN = String.fromCharCode(0xe000);
const CLOSE = String.fromCharCode(0xe001);
const SENTINEL = /\uE000(\d+)\uE001/g;
const SENTINEL_CHARS = /[\uE000\uE001]/g;
const DEFINITION_LINE = /^[ \t]*\[(\d+)\]:[ \t]+\S.*$/gm;
const INLINE_LINK = /\[(\d+)\]\([^\s()]*(?:[ \t]+"[^"]*")?\)/g;

/**
 * The source offsets of every code block and code span, from the same parser
 * that will render the text.
 *
 * A sentinel is reversible wherever it lands — `remarkReferences` turns one
 * back into a literal `[n]` inside code, a link or a URL — but a deletion
 * cannot be undone in the tree, because by then the characters are gone. Only
 * the parser knows for certain where code starts and ends (a fence may open
 * with three or thirty backticks, close late or not at all, and a span may run
 * across lines), so the deletions are filtered against what it reports rather
 * than against a regex guess.
 */
function codeRanges(text: string): Array<readonly [number, number]> {
	const ranges: Array<readonly [number, number]> = [];
	const visit = (node: MdNode) => {
		if (node.type === "code" || node.type === "inlineCode") {
			const at = node.position as
				| { start?: { offset?: number }; end?: { offset?: number } }
				| undefined;
			const start = at?.start?.offset;
			const end = at?.end?.offset;
			if (typeof start === "number" && typeof end === "number") {
				ranges.push([start, end]);
			}
			return;
		}
		for (const child of node.children ?? []) visit(child);
	};
	visit(
		fromMarkdown(text, {
			extensions: [gfm()],
			mdastExtensions: [gfmFromMarkdown()],
		}) as unknown as MdNode,
	);
	return ranges;
}

/** Swap each cited `[n]` for a sentinel; see the file comment for the rest. */
export function protectReferences(
	text: string,
	markers: ReadonlySet<number>,
): string {
	const cited = (n: string) => markers.has(Number(n));
	const source = text.replace(SENTINEL_CHARS, "");

	// The two irreversible edits, as offsets into `source`: a cited marker's
	// definition line, and the `(url)` that follows its inline `[n]`.
	const cuts: Array<readonly [number, number]> = [];
	for (const match of source.matchAll(DEFINITION_LINE)) {
		const n = match[1];
		if (n !== undefined && match.index !== undefined && cited(n)) {
			cuts.push([match.index, match.index + match[0].length]);
		}
	}
	for (const match of source.matchAll(INLINE_LINK)) {
		const n = match[1];
		if (n !== undefined && match.index !== undefined && cited(n)) {
			// The `[n]` stays and becomes a sentinel below; only what follows goes.
			cuts.push([match.index + n.length + 2, match.index + match[0].length]);
		}
	}

	let kept = source;
	if (cuts.length > 0) {
		// Parsing is paid for only when there is something to delete: an answer
		// that merely cites `[1]` never reaches here.
		const code = codeRanges(source);
		const outside = cuts
			.filter(([start, end]) =>
				code.every(([from, to]) => start >= to || end <= from),
			)
			// Back to front, so an earlier cut's offsets stay valid.
			.sort((a, b) => b[0] - a[0]);
		for (const [start, end] of outside) {
			kept = kept.slice(0, start) + kept.slice(end);
		}
	}
	return kept.replace(MARKER, (whole, n: string) =>
		cited(n) ? `${OPEN}${n}${CLOSE}` : whole,
	);
}

const restore = (value: string) => value.replace(SENTINEL, "[$1]");

interface MdNode {
	type: string;
	value?: string;
	children?: MdNode[];
	data?: unknown;
	[key: string]: unknown;
}

const STRING_FIELDS = ["url", "title", "alt", "label", "identifier"] as const;

/** Remark plugin: lift sentinels in ordinary text into reference elements. */
export function remarkReferences() {
	return (tree: MdNode) => {
		const visit = (node: MdNode, inLink: boolean) => {
			if (node.type !== "text" && typeof node.value === "string") {
				node.value = restore(node.value);
			}
			for (const key of STRING_FIELDS) {
				const value = node[key];
				if (typeof value === "string") node[key] = restore(value);
			}
			if (!node.children) return;
			const linked =
				inLink || node.type === "link" || node.type === "linkReference";
			const next: MdNode[] = [];
			for (const child of node.children) {
				if (child.type !== "text") {
					visit(child, linked);
					next.push(child);
					continue;
				}
				const value = child.value ?? "";
				if (linked) {
					next.push({ type: "text", value: restore(value) });
					continue;
				}
				let last = 0;
				for (const match of value.matchAll(SENTINEL)) {
					const index = match.index ?? 0;
					if (index > last) {
						next.push({ type: "text", value: value.slice(last, index) });
					}
					next.push({
						type: "contentReference",
						data: {
							hName: REFERENCE_ELEMENT,
							hProperties: { marker: Number(match[1]) },
						},
					});
					last = index + match[0].length;
				}
				if (last === 0) next.push(child);
				else if (last < value.length) {
					next.push({ type: "text", value: value.slice(last) });
				}
			}
			node.children = next;
		};
		visit(tree, false);
	};
}

/** The source range a node covers, when the parser recorded one. */
function sourceSpan(node: MdNode): [number, number] | undefined {
	const at = node.position as
		| { start?: { offset?: number }; end?: { offset?: number } }
		| undefined;
	const start = at?.start?.offset;
	const end = at?.end?.offset;
	return typeof start === "number" && typeof end === "number" && start <= end
		? [start, end]
		: undefined;
}

// Only the offsets are read downstream, but a unist point is 1-based and
// `mdast-util-to-hast` drops a position whose line is 0 wholesale — taking the
// offsets with it. So the line and column are the smallest valid pair rather
// than a recount of where the break landed.
const span = (start: number, end: number) => ({
	position: {
		start: { line: 1, column: 1, offset: start },
		end: { line: 1, column: 1, offset: end },
	},
});

/**
 * Remark plugin: a single newline inside a paragraph is a line break, the way
 * chat and model output are written, rather than CommonMark's soft wrap.
 *
 * Each piece keeps the bytes of the source it came from. A split that dropped
 * them would leave the renderer unable to say which bytes a run was rendered
 * from (`Markdown`'s `sourceOffsets`), and a run with no position maps to
 * nothing — so the whole paragraph would silently lose its anchor the moment a
 * caller asked for line breaks.
 */
export function remarkLineBreaks() {
	return (tree: MdNode, file?: unknown) => {
		// The source the parser read. Each piece is LOCATED in it rather than
		// counted along the parsed text: a text node's value is not its source —
		// the parser drops a line's indentation and a soft break's trailing space,
		// and decodes `&amp;` to one character — so counting along the value puts
		// every piece after the first difference on bytes that are not its text.
		const source = file === undefined || file === null ? "" : String(file);
		const visit = (node: MdNode) => {
			if (!node.children) return;
			const next: MdNode[] = [];
			for (const child of node.children) {
				if (child.type !== "text" || !child.value?.includes("\n")) {
					visit(child);
					next.push(child);
					continue;
				}
				const at = sourceSpan(child);
				// Split keeping the separators, so a `\r\n` is one break like a `\n`.
				const pieces = child.value.split(/(\r?\n)/);
				let cursor = at?.[0] ?? 0;
				// Once a piece cannot be found, no later one can be trusted either:
				// the cursor never advanced past it, so a search could match BEFORE
				// the piece's real place and claim bytes earlier in the node. From
				// there on every piece gets the whole node's range, which the
				// renderer then marks inexact — coarse, and true.
				let located = at !== undefined && source !== "";
				for (const [index, piece] of pieces.entries()) {
					if (index % 2 === 1) {
						next.push({ type: "break" });
						continue;
					}
					if (piece === "") continue;
					let found = -1;
					if (located) {
						found = source.indexOf(piece, cursor);
						if (found < 0 || !at || found + piece.length > at[1]) {
							located = false;
							found = -1;
						} else cursor = found + piece.length;
					}
					next.push({
						type: "text",
						value: piece,
						...(found >= 0
							? span(found, found + piece.length)
							: at
								? span(at[0], at[1])
								: {}),
					});
				}
			}
			node.children = next;
		};
		visit(tree);
	};
}
