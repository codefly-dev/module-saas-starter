// One-line plain text for the `inline` variant: a list row or a citation excerpt
// shows what the content SAYS, never its markup. Markdown is parsed (not
// regex-stripped) so a link keeps its words, a table keeps its cells, and a
// construct the stripper did not anticipate cannot leak syntax into the row.

import type { Nodes, Parent } from "mdast";
import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";
import { type ContentFormat, detectFormat, readJson } from "./detect.js";

// An inline rendering is one line, so nothing past a few screens of source can
// ever show. Bounding the parse keeps a list of large payloads cheap.
const INLINE_SOURCE_LIMIT = 8_000;

// Block-level nodes end a thought: joining them needs a separator, where inline
// siblings (`**a**b`) must not gain one.
const BLOCK_TYPES: ReadonlySet<string> = new Set([
	"paragraph",
	"heading",
	"blockquote",
	"list",
	"listItem",
	"code",
	"table",
	"tableRow",
	"thematicBreak",
	"footnoteDefinition",
]);

function collapse(text: string): string {
	return text.replace(/\s+/g, " ").trim();
}

function textOf(node: Nodes): string {
	switch (node.type) {
		case "text":
		case "inlineCode":
		case "code":
			return node.value;
		case "image":
		case "imageReference":
			return node.alt ?? "";
		case "break":
			return " ";
		// Raw HTML never becomes text either: it is dropped, as the block
		// renderer drops it.
		case "html":
			return "";
		case "tableCell":
			return childrenText(node);
		case "tableRow":
			return node.children.map((cell) => textOf(cell)).join(" · ");
		default:
			return "children" in node ? childrenText(node as Parent) : "";
	}
}

function childrenText(parent: Parent): string {
	let out = "";
	for (const child of parent.children as Nodes[]) {
		const text = textOf(child);
		if (text === "") continue;
		out += BLOCK_TYPES.has(child.type) && out !== "" ? ` ${text}` : text;
	}
	return out;
}

/** Markdown source as the words it renders, on one line. */
export function markdownToPlainText(source: string): string {
	const tree = fromMarkdown(source.slice(0, INLINE_SOURCE_LIMIT), {
		extensions: [gfm()],
		mdastExtensions: [gfmFromMarkdown()],
	});
	return collapse(childrenText(tree));
}

function compactJson(value: unknown): string {
	try {
		return JSON.stringify(value) ?? String(value);
	} catch {
		// A cycle or a BigInt: say what it is rather than throw inside a row.
		return String(value);
	}
}

/**
 * Any content as one line of plain text: markdown loses its markup, JSON is
 * compacted, and text and code have their whitespace collapsed.
 */
export function toPlainText(
	value: unknown,
	format: ContentFormat = "auto",
): string {
	const resolved = format === "auto" ? detectFormat(value) : format;
	if (resolved === "json") {
		const reading = readJson(value);
		const text = reading.ok ? compactJson(reading.value) : String(value ?? "");
		return collapse(text.slice(0, INLINE_SOURCE_LIMIT));
	}
	const source =
		typeof value === "string" ? value : value == null ? "" : compactJson(value);
	if (resolved === "markdown") return markdownToPlainText(source);
	return collapse(source.slice(0, INLINE_SOURCE_LIMIT));
}
