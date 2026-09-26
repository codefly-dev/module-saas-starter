"use client";

import { type ReactNode, useEffect, useState } from "react";
import { cn } from "../layout/cn.js";
import { CopyButton } from "./copy-button.js";

export interface CodeBlockProps {
	code: string;
	/** A language name or alias (`ts`, `python`, `json`, ...). Unknown → plain. */
	language?: string;
	/** Syntax-highlight when the language is known. Default true. */
	highlight?: boolean;
	/** Show a copy button. Default true. */
	copyable?: boolean;
	/** Wrap long lines instead of scrolling horizontally. Default false. */
	wrap?: boolean;
	className?: string;
}

interface Highlighted {
	readonly key: string;
	readonly node: ReactNode;
}

/**
 * Monospace code, with optional syntax highlighting. The highlighter is a
 * separate chunk loaded the first time a block asks for it; until it arrives
 * (and whenever the language is unknown) the code renders plain, so nothing
 * waits on it and nothing breaks without it.
 */
export function CodeBlock({
	code,
	language,
	highlight = true,
	copyable = true,
	wrap = false,
	className,
}: CodeBlockProps) {
	const lang = language?.trim().toLowerCase() || undefined;
	const key = `${lang ?? ""}\u0000${code}`;
	const [highlighted, setHighlighted] = useState<Highlighted | null>(null);

	useEffect(() => {
		if (!highlight || !lang) return;
		let cancelled = false;
		import("./highlight.js")
			.then((module) => {
				if (cancelled) return;
				const node = module.highlight(code, lang);
				if (node !== null) setHighlighted({ key, node });
			})
			.catch(() => {
				// A failed chunk load leaves the block plain; the code is still there.
			});
		return () => {
			cancelled = true;
		};
	}, [code, lang, key, highlight]);

	// A stale highlight (from before `code` changed) is never shown.
	const body = highlight && highlighted?.key === key ? highlighted.node : code;

	return (
		<div
			data-slot="content-code"
			className={cn(
				"group/code relative rounded-md border border-border bg-muted/40",
				className,
			)}
		>
			{copyable && (
				<div className="absolute top-1 right-1 opacity-100 sm:opacity-0 sm:group-hover/code:opacity-100 sm:group-focus-within/code:opacity-100">
					<CopyButton text={code} label="code" />
				</div>
			)}
			<pre
				className={cn(
					"overflow-x-auto p-3 font-mono type-caption-plain text-foreground",
					wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre",
					copyable && "pr-10",
				)}
			>
				<code data-language={lang}>{body}</code>
			</pre>
		</div>
	);
}
