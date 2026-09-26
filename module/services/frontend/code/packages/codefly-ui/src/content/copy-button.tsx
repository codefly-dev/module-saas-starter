"use client";

import { Check, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "../layout/button.js";
import { cn } from "../layout/cn.js";

export interface CopyButtonProps {
	/** The exact text written to the clipboard. Resolved on click, not render. */
	text: string | (() => string);
	/** What is copied, for the accessible name: "Copy {label}". */
	label?: string;
	className?: string;
}

type CopyState = "idle" | "copied" | "failed";

// How long the confirmation stays up before the control reads "Copy" again.
const CONFIRMATION_MS = 1_500;

/**
 * An icon button that copies text and says so. The accessible name names what
 * it copies, and the outcome is announced through a polite live region, so a
 * screen-reader user hears "Copied" the way a sighted user sees the tick.
 */
export function CopyButton({
	text,
	label = "content",
	className,
}: CopyButtonProps) {
	const [state, setState] = useState<CopyState>("idle");

	useEffect(() => {
		if (state === "idle") return;
		const timer = setTimeout(() => setState("idle"), CONFIRMATION_MS);
		return () => clearTimeout(timer);
	}, [state]);

	async function copy() {
		try {
			const value = typeof text === "function" ? text() : text;
			await navigator.clipboard.writeText(value);
			setState("copied");
		} catch {
			setState("failed");
		}
	}

	return (
		<>
			<Button
				type="button"
				variant="ghost"
				size="icon-xs"
				aria-label={`Copy ${label}`}
				title={`Copy ${label}`}
				data-slot="content-copy"
				className={cn("text-muted-foreground hover:text-foreground", className)}
				onClick={copy}
			>
				{state === "copied" ? (
					<Check aria-hidden="true" />
				) : (
					<Copy aria-hidden="true" />
				)}
			</Button>
			<span role="status" aria-live="polite" className="sr-only">
				{state === "copied"
					? "Copied"
					: state === "failed"
						? "Copy failed"
						: ""}
			</span>
		</>
	);
}
