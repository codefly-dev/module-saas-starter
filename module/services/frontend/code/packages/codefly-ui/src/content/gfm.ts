// GitHub-flavoured markdown for the renderer, parse side only.
//
// `remark-gfm` registers GFM for both directions: parsing (micromark syntax +
// mdast construction) and serialising back to markdown. The content tier only
// ever parses, and the serialiser drags `mdast-util-to-markdown` into every page
// that renders a chat answer. This plugin registers exactly the parse half —
// the same two extensions `remark-gfm` uses, from the same packages — so the
// rendered tree is identical and the bundle is not.

import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";

interface ParserData {
	micromarkExtensions?: unknown[];
	fromMarkdownExtensions?: unknown[];
}

/** A unified (remark) plugin: tables, task lists, strikethrough, autolinks, footnotes. */
export function remarkGfmParse(this: unknown): void {
	// `this` is the unified processor; only its `data()` store is touched.
	const data = (this as { data(): ParserData }).data();
	if (!data.micromarkExtensions) data.micromarkExtensions = [];
	if (!data.fromMarkdownExtensions) data.fromMarkdownExtensions = [];
	data.micromarkExtensions.push(gfm());
	data.fromMarkdownExtensions.push(gfmFromMarkdown());
}
