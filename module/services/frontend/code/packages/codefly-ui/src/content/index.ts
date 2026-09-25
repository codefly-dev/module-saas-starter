// The content tier: one way to render text the host did not write — ingested
// documents, model output, payloads — as markdown, JSON, code or plain text,
// block or inline. Exported from `@codefly-dev/ui/content` (a client subpath),
// shared as a Module-Federation singleton so the host and every remote render
// content from one instance, with one copy of the markdown parser.
//
// Security posture (see markdown.tsx and url.ts): no raw HTML, links only for
// http/https/mailto, images off unless opted in, no innerHTML anywhere.

export { CodeBlock, type CodeBlockProps } from "./code-block.js";
export { Content, type ContentProps, type ContentVariant } from "./content.js";
export { CopyButton, type CopyButtonProps } from "./copy-button.js";
export {
	type ContentFormat,
	detectFormat,
	type JsonReading,
	looksLikeMarkdown,
	type ResolvedContentFormat,
	readJson,
} from "./detect.js";
export { JsonView, type JsonViewProps, stringifyJson } from "./json-view.js";
export { type HeadingLevel, Markdown, type MarkdownProps } from "./markdown.js";
export { markdownToPlainText, toPlainText } from "./plain.js";
export { TextBlock, type TextBlockProps } from "./text-block.js";
export { safeImageUrl, safeLinkUrl } from "./url.js";
