// Syntax highlighting, loaded on demand. `<CodeBlock>` imports this module
// dynamically, so the grammars and the highlighter reach a page only when a
// highlighted block actually renders — the markdown and JSON paths never pay for
// them.
//
// The highlighter (lowlight, highlight.js grammars) produces a hast tree, and
// the tree becomes React elements through `hast-util-to-jsx-runtime`: no HTML
// string, no `dangerouslySetInnerHTML`. Its class names are rewritten to the
// host's token utilities, so a skin re-colours code the way it re-colours
// everything else and the kit ships no theme of its own.

import type { Element, ElementContent, Root } from "hast";
import { toJsxRuntime } from "hast-util-to-jsx-runtime";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import go from "highlight.js/lib/languages/go";
import java from "highlight.js/lib/languages/java";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import markdown from "highlight.js/lib/languages/markdown";
import python from "highlight.js/lib/languages/python";
import rust from "highlight.js/lib/languages/rust";
import shell from "highlight.js/lib/languages/shell";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";
import { createLowlight } from "lowlight";
import type { ReactNode } from "react";
import { Fragment, jsx, jsxs } from "react/jsx-runtime";
import { TONE } from "./tones.js";

// A deliberate, small set: the languages content in a SaaS product actually
// carries (payloads, config, snippets). Each grammar's own aliases come with it
// (`js`, `ts`, `py`, `sh`, `yml`, `html`, `golang`, ...).
const lowlight = createLowlight({
	bash,
	css,
	diff,
	go,
	java,
	javascript,
	json,
	markdown,
	python,
	rust,
	shell,
	sql,
	typescript,
	xml,
	yaml,
});

// Past this, highlighting costs more than it gives; the block renders plain.
export const HIGHLIGHT_SOURCE_LIMIT = 100_000;

// highlight.js scope → token-derived tone (see tones.ts). The skin owns every
// colour; the kit ships no theme of its own.
const SCOPE_CLASS: Readonly<Record<string, string>> = {
	keyword: TONE.keyword,
	"selector-tag": TONE.keyword,
	doctag: TONE.keyword,
	literal: TONE.number,
	number: TONE.number,
	symbol: TONE.number,
	bullet: TONE.number,
	string: TONE.string,
	regexp: TONE.string,
	addition: TONE.string,
	"template-tag": TONE.string,
	title: TONE.title,
	section: TONE.title,
	name: TONE.title,
	"selector-id": TONE.title,
	"selector-class": TONE.title,
	attr: TONE.attribute,
	attribute: TONE.attribute,
	property: TONE.attribute,
	variable: TONE.attribute,
	"template-variable": TONE.attribute,
	params: TONE.attribute,
	built_in: TONE.attribute,
	type: TONE.attribute,
	comment: `${TONE.muted} italic`,
	quote: `${TONE.muted} italic`,
	meta: TONE.muted,
	deletion: TONE.deletion,
	emphasis: "italic",
};

function tokenClass(className: unknown): string[] | undefined {
	if (!Array.isArray(className)) return undefined;
	for (const name of className) {
		if (typeof name !== "string" || !name.startsWith("hljs-")) continue;
		const mapped = SCOPE_CLASS[name.slice("hljs-".length)];
		if (mapped) return mapped.split(" ");
	}
	return undefined;
}

function retheme(nodes: ElementContent[]): ElementContent[] {
	return nodes.map((node) => {
		if (node.type !== "element") return node;
		const element: Element = {
			...node,
			properties: { className: tokenClass(node.properties.className) },
			children: retheme(node.children),
		};
		return element;
	});
}

/** Whether a language name (or alias) has a registered grammar. */
export function isHighlightable(language: string | undefined): boolean {
	return !!language && lowlight.registered(language);
}

/**
 * The highlighted rendering of `code`, or `null` when the language is unknown
 * or the source is too large to be worth it (the caller renders it plain).
 */
export function highlight(
	code: string,
	language: string | undefined,
): ReactNode | null {
	if (!language || code.length > HIGHLIGHT_SOURCE_LIMIT) return null;
	if (!lowlight.registered(language)) return null;
	const tree = lowlight.highlight(language, code);
	const themed: Root = {
		type: "root",
		children: retheme(tree.children as ElementContent[]),
	};
	return toJsxRuntime(themed, { Fragment, jsx, jsxs });
}
