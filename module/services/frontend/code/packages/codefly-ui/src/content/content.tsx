"use client";

import { cn } from "../layout/cn.js";
import { CodeBlock } from "./code-block.js";
import { type ContentFormat, detectFormat, readJson } from "./detect.js";
import { JsonView, stringifyJson } from "./json-view.js";
import { type HeadingLevel, Markdown } from "./markdown.js";
import { toPlainText } from "./plain.js";
import { TextBlock } from "./text-block.js";

/** `block` renders the content; `inline` renders one line of its plain text. */
export type ContentVariant = "block" | "inline";

export interface ContentProps {
	/** What to render: a string (markdown, JSON, code, text) or structured data. */
	value: unknown;
	/** How to read `value`. Default `auto`: parseable JSON → json, markdown markers → markdown, else text. */
	format?: ContentFormat;
	/** Default `block`. `inline` is for list rows and excerpts: markup stripped, one line, ellipsis. */
	variant?: ContentVariant;
	/** Inline only: lines shown before the ellipsis. Default 1. */
	lines?: 1 | 2 | 3;
	/** Code only: the language to highlight (`ts`, `python`, `json`, ...). */
	language?: string;
	/** Markdown only: the HTML level a `#` renders as. Default 3. */
	headingLevel?: HeadingLevel;
	/** Markdown only: render https images. Default false (alt text, nothing fetched). */
	allowImages?: boolean;
	/** JSON only: levels open on first render. Default 1. */
	expandDepth?: number;
	/** Code only: wrap long lines instead of scrolling. Default false. */
	wrap?: boolean;
	/** JSON and code: show a copy button. Default true. */
	copyable?: boolean;
	/** JSON only: accessible name for the tree. */
	label?: string;
	className?: string;
}

const LINE_CLAMP = { 2: "line-clamp-2", 3: "line-clamp-3" } as const;

/**
 * The one way to put text the host did not write on screen. It renders
 * markdown without raw HTML, JSON as a bounded collapsible tree, code with
 * lazily-loaded highlighting, and plain text with its whitespace intact — or,
 * `inline`, any of them as a single line of plain text.
 */
export function Content({
	value,
	format = "auto",
	variant = "block",
	lines = 1,
	language,
	headingLevel,
	allowImages,
	expandDepth,
	copyable,
	wrap,
	label,
	className,
}: ContentProps) {
	const resolved = format === "auto" ? detectFormat(value) : format;

	if (variant === "inline") {
		const text = toPlainText(value, resolved);
		return (
			<span
				data-slot="content-inline"
				title={text}
				className={cn(
					"block min-w-0",
					lines === 1
						? "truncate"
						: cn("[overflow-wrap:anywhere]", LINE_CLAMP[lines]),
					className,
				)}
			>
				{text}
			</span>
		);
	}

	if (resolved === "json") {
		const reading = readJson(value);
		if (!reading.ok) {
			return (
				<TextBlock
					text={typeof value === "string" ? value : ""}
					className={className}
				/>
			);
		}
		return (
			<JsonView
				value={reading.value}
				expandDepth={expandDepth}
				copyable={copyable}
				label={label}
				className={className}
			/>
		);
	}

	const source =
		typeof value === "string"
			? value
			: value == null
				? ""
				: stringifyJson(value);
	if (resolved === "markdown") {
		return (
			<Markdown
				headingLevel={headingLevel}
				allowImages={allowImages}
				className={className}
			>
				{source}
			</Markdown>
		);
	}
	if (resolved === "code") {
		return (
			<CodeBlock
				code={source}
				language={language}
				copyable={copyable}
				wrap={wrap}
				className={className}
			/>
		);
	}
	return <TextBlock text={source} className={className} />;
}
