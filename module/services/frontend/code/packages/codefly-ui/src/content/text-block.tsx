import { cn } from "../layout/cn.js";

export interface TextBlockProps {
	text: string;
	className?: string;
}

/**
 * Plain text exactly as written: newlines and runs of spaces are kept, long
 * lines wrap, and an unbroken token (a URL, a hash) breaks rather than
 * overflowing its container. React escapes the text, so markup in it is shown,
 * never interpreted.
 */
export function TextBlock({ text, className }: TextBlockProps) {
	return (
		<div
			data-slot="content-text"
			className={cn(
				"whitespace-pre-wrap break-words [overflow-wrap:anywhere]",
				className,
			)}
		>
			{text}
		</div>
	);
}
