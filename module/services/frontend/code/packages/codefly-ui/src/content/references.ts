// Numbered references inside markdown: a grounded answer cites its sources as
// `[1]`, `[2]`, and the caller renders each marker as whatever it links to (a
// popover, a jump to the source). A cited `[n]` is a reference before it is
// markdown. CommonMark would read `[1](url)` as a link and `[1]` as a reference
// to a `[1]: url` definition, both of which a model routinely emits, so:
//
//   - before parsing, each cited `[n]` becomes a private-use sentinel, a cited
//     marker's `[n]: url` definition line is dropped, and the `(url)` of a cited
//     `[n](url)` is dropped (the reference is the source, not the model's URL);
//   - after parsing, sentinels in ordinary text become a `content-reference`
//     element the renderer maps to the caller's component, and everywhere else
//     (code, link text, URLs, titles, alt text) they turn back into a literal
//     `[n]`, so a `[2]` in a code span stays literal and a marker never nests
//     inside a link.
//
// A bracketed number that is not a reference is untouched.

/** The hast element name a reference marker lowers to. */
export const REFERENCE_ELEMENT = "content-reference";

const MARKER = /\[(\d+)\]/g;
const OPEN = String.fromCharCode(0xe000);
const CLOSE = String.fromCharCode(0xe001);
const SENTINEL = new RegExp("\\uE000(\\d+)\\uE001", "g");
const SENTINEL_CHARS = new RegExp("[\\uE000\\uE001]", "g");
const DEFINITION_LINE = /^[ \t]*\[(\d+)\]:[ \t]+\S.*$/gm;
const INLINE_LINK = /\[(\d+)\]\([^\s()]*(?:[ \t]+"[^"]*")?\)/g;

/** Swap each cited `[n]` for a sentinel; see the file comment for the rest. */
export function protectReferences(
	text: string,
	markers: ReadonlySet<number>,
): string {
	const cited = (n: string) => markers.has(Number(n));
	return text
		.replace(SENTINEL_CHARS, "")
		.replace(DEFINITION_LINE, (whole, n: string) => (cited(n) ? "" : whole))
		.replace(INLINE_LINK, (whole, n: string) => (cited(n) ? `[${n}]` : whole))
		.replace(MARKER, (whole, n: string) =>
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
