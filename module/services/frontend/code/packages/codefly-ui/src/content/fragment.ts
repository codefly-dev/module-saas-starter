// A SLICE of markdown read as the prose it renders to: a search hit's chunk, a
// cited passage, the window around a quote. A slice is not a document. It can
// open inside a YAML frontmatter block, inside a table row or halfway through an
// emphasis, so handing it to a markdown parser renders the leftovers literally
// (`Solution** | …`) or misreads them. Nothing here parses: every rule rewrites
// source characters into readable ones, line by line, and the words stay
// exactly as written.
//
// Use `markdownToPlainText` (plain.ts) for a whole document and this for a
// slice of one.

const YAML_KEY = /^[A-Za-z_][\w.-]*\s*:/;
const FENCE = /^\s*(?:`{3,}|~{3,})/;
const RULE = /^\s*([-*_])(?:\s*\1){2,}\s*$/;
const ALIGNMENT_ROW = /^[\s|:-]*-{3,}[\s|:-]*$/;
const BLOCK_MARKER =
	/^\s*(?:>\s?|[-*+]\s+(?=\S)|\d{1,9}[.)]\s+(?=\S)|#{1,6}\s+)/;

// A backslash escape is literal text: hide it from every rule, restore it last.
// Each escaped character becomes a private-use code point (U+E100 plus its
// index in ESCAPE_SET), so no rule below can match it.
const ESCAPE_SET = "\\`*_{}[]()#+-.!|>~";
const ESCAPABLE = /\\([\\`*_{}[\]()#+\-.!|>~])/g;
const ESCAPED = new RegExp("[\\uE100-\\uE11F]", "gu");
const hide = (_: string, character: string) =>
	String.fromCharCode(0xe100 + ESCAPE_SET.indexOf(character));
const reveal = (code: string) => ESCAPE_SET[code.charCodeAt(0) - 0xe100] ?? "";

/**
 * Drop a leading YAML frontmatter block. The key line is what distinguishes it
 * from a slice that opens on a thematic break; with no closing delimiter the
 * slice ended inside the block, so all of it is frontmatter.
 */
function withoutFrontmatter(lines: string[]): string[] {
	if (lines[0]?.trim() !== "---") return lines;
	const first = lines.slice(1).find((line) => line.trim());
	if (!first || !YAML_KEY.test(first.trim())) return lines;
	for (let index = 1; index < lines.length; index++) {
		const line = lines[index]?.trim();
		if (line === "---" || line === "...") return lines.slice(index + 1);
	}
	return [];
}

function unwrapBlock(line: string): string {
	let text = line;
	for (let previous = ""; text !== previous; ) {
		previous = text;
		text = text.replace(BLOCK_MARKER, "");
	}
	return text;
}

/** A table row (or the tail of one a slice cut into) as scannable cells. */
function unwrapRow(line: string): string {
	if (ALIGNMENT_ROW.test(line)) return "";
	return line
		.split("|")
		.map((cell) => cell.trim())
		.filter(Boolean)
		.join(" · ");
}

function unwrapInline(text: string): string {
	return (
		text
			// Images and links read as their text; an autolink as its URL.
			.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
			.replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
			.replace(/\[([^\]]*)\]\[[^\]]*\]/g, "$1")
			.replace(/<((?:https?|mailto):[^>\s]+)>/g, "$1")
			// Emphasis, strikethrough and code markers, paired or left dangling by
			// the cut. The words inside stay.
			.replace(/\*{1,3}|~~|`+/g, "")
			// An underscore between two word characters is part of a name
			// (`snake_case`), not emphasis.
			.replace(/(^|[^\p{L}\p{N}])_{1,3}(?=\S)/gu, "$1")
			.replace(/(\S)_{1,3}(?=$|[^\p{L}\p{N}])/gu, "$1")
	);
}

/** A slice of markdown as one line of the words it renders to. */
export function markdownFragmentToText(fragment: string): string {
	const out: string[] = [];
	const source = fragment.replace(ESCAPED, "").replace(ESCAPABLE, hide);
	for (const line of withoutFrontmatter(source.split(/\r?\n/))) {
		if (FENCE.test(line) || RULE.test(line)) continue;
		const block = unwrapBlock(line);
		const text = unwrapInline(block.includes("|") ? unwrapRow(block) : block);
		if (text.trim()) out.push(text.trim());
	}
	return out
		.join(" ")
		.replace(ESCAPED, reveal)
		.replace(/\s+/gu, " ")
		.replace(/^(?:\s*·\s*)+|(?:\s*·\s*)+$/g, "")
		.trim();
}
