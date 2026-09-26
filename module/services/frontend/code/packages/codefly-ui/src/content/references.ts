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
const SENTINEL = new RegExp("\\uE000(\\d+)\\uE001", "g");
const SENTINEL_CHARS = new RegExp("[\\uE000\\uE001]", "g");
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

/**
 * Remark plugin: a single newline inside a paragraph is a line break, the way
 * chat and model output are written, rather than CommonMark's soft wrap.
 */
export function remarkLineBreaks() {
	return (tree: MdNode) => {
		const visit = (node: MdNode) => {
			if (!node.children) return;
			const next: MdNode[] = [];
			for (const child of node.children) {
				if (child.type !== "text" || !child.value?.includes("\n")) {
					visit(child);
					next.push(child);
					continue;
				}
				const lines = child.value.split(/\r?\n/);
				lines.forEach((line, index) => {
					if (index > 0) next.push({ type: "break" });
					if (line !== "") next.push({ type: "text", value: line });
				});
			}
			node.children = next;
		};
		visit(tree);
	};
}
