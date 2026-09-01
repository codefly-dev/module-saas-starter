"use client";

// The shared, pure <Chat> composite. It takes a fully-resolved message list and
// paints a themed conversation with a composer — it opens no socket, reads no
// host context, and imports no app code, so the host app and a solution's
// Module-Federation remote render identical chat from one package instance.
// Streaming and state are the job of `@codefly/saas-sdk`'s `useChatStream`, which
// owns the SSE/WS transport and feeds `messages`/`onSend` in.

import {
	type FormEvent,
	type KeyboardEvent,
	type ReactNode,
	useState,
} from "react";
import { Section } from "../layout/card.js";
import { cn } from "./cn.js";
import type { ChatMessage, ChatRole } from "./types.js";

const ROLE_LABEL: Record<ChatRole, string> = {
	user: "You",
	assistant: "Assistant",
	system: "System",
};

function initials(label: string): string {
	const words = label.trim().split(/\s+/).filter(Boolean);
	if (words.length === 0) return "?";
	if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
	return (words[0][0] + words[words.length - 1][0]).toUpperCase();
}

function Avatar({ label }: { label: string }) {
	return (
		<div
			aria-hidden="true"
			className="flex h-8 w-8 shrink-0 select-none items-center justify-center rounded-full bg-muted text-xs font-medium text-muted-foreground"
		>
			{initials(label)}
		</div>
	);
}

function MessageBubble({ message }: { message: ChatMessage }) {
	const author = message.author ?? ROLE_LABEL[message.role];
	const isUser = message.role === "user";
	return (
		<div className={cn("flex gap-3", isUser && "flex-row-reverse")}>
			<Avatar label={author} />
			<div
				className={cn("flex max-w-[75%] flex-col gap-1", isUser && "items-end")}
			>
				<span className="text-xs font-medium text-muted-foreground">
					{author}
				</span>
				<div
					className={cn(
						"whitespace-pre-wrap rounded-lg px-3 py-2 text-sm",
						isUser
							? "bg-primary text-primary-foreground"
							: "bg-muted text-foreground",
					)}
				>
					{message.content}
					{message.pending && (
						// Decorative: the log's `aria-busy` already tells assistive tech a
						// reply is in flight. A `role="status"`/`aria-label` here would be a
						// second live region nested in the log, announcing "typing" on top
						// of the streamed text.
						<span
							aria-hidden="true"
							data-testid="typing-indicator"
							className="ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-current align-text-bottom"
						/>
					)}
				</div>
			</div>
		</div>
	);
}

function Composer({
	onSend,
	busy,
	placeholder,
}: {
	onSend: (text: string) => void;
	busy?: boolean;
	placeholder?: string;
}) {
	const [value, setValue] = useState("");

	function submit() {
		const text = value.trim();
		if (!text || busy) return;
		onSend(text);
		setValue("");
	}

	function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
		// Enter sends; Shift+Enter inserts a newline, the convention every chat
		// composer follows.
		if (event.key === "Enter" && !event.shiftKey) {
			event.preventDefault();
			submit();
		}
	}

	function onSubmit(event: FormEvent) {
		event.preventDefault();
		submit();
	}

	return (
		<form onSubmit={onSubmit} className="flex items-end gap-2 border-t p-3">
			<textarea
				value={value}
				onChange={(event) => setValue(event.target.value)}
				onKeyDown={onKeyDown}
				rows={1}
				placeholder={placeholder ?? "Send a message…"}
				aria-label="Message"
				className="min-h-9 flex-1 resize-none rounded-md border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
			/>
			<button
				type="submit"
				disabled={busy || value.trim() === ""}
				className="inline-flex h-9 items-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50"
			>
				Send
			</button>
		</form>
	);
}

export interface ChatProps {
	messages: ChatMessage[];
	/** Called with the trimmed text when the user submits. Omit to render read-only. */
	onSend?: (text: string) => void;
	/** A reply is streaming: the default composer disables its send while true. */
	busy?: boolean;
	/** Placeholder for the default composer. */
	placeholder?: string;
	/** Replace the default composer entirely (e.g. attachments, slash commands). */
	composer?: ReactNode;
	/** Rendered in place of the transcript while there are no messages. */
	emptyState?: ReactNode;
	title?: ReactNode;
	description?: ReactNode;
	className?: string;
}

/**
 * Render a conversation with a composer. Pass `messages` (from your own state or
 * from `@codefly/saas-sdk`'s `useChatStream`) and an `onSend` handler. The
 * component is pure presentation — wrap it, restyle it via tokens, or swap the
 * `composer` freely.
 */
export function Chat({
	messages,
	onSend,
	busy,
	placeholder,
	composer,
	emptyState,
	title,
	description,
	className,
}: ChatProps) {
	return (
		<div
			className={cn(
				"flex h-full flex-col overflow-hidden rounded-lg border bg-card text-card-foreground shadow-sm",
				className,
			)}
		>
			{/* The header is a titled block, so compose `Section` rather than
			    re-inline its heading class string; the outer guard keeps the
			    bordered bar from rendering empty when no title/description is set. */}
			{(title || description) && (
				<Section
					title={title}
					description={description}
					className="border-b p-4"
				/>
			)}
			{/* `role="log"` already implies `aria-live="polite"`, so it is not
			    repeated. `aria-busy` while a reply streams tells assistive tech to
			    hold announcements until the token-by-token text settles, then read
			    the finished message once instead of on every delta. */}
			<div
				role="log"
				aria-busy={busy}
				className="flex flex-1 flex-col gap-4 overflow-y-auto p-4"
			>
				{messages.length === 0
					? (emptyState ?? (
							<div className="m-auto text-sm text-muted-foreground">
								No messages yet.
							</div>
						))
					: messages.map((message) => (
							<MessageBubble key={message.id} message={message} />
						))}
			</div>
			{/* An explicitly-passed `composer` always wins — including `null`/`false`
			    to render no composer at all. The default composer appears only when
			    the prop is omitted and there is an `onSend` to drive it. (Using `??`
			    here would let `composer={null}` fall through to the default, while
			    `composer={false}` suppressed it — an inconsistent sentinel.) */}
			{composer === undefined
				? onSend && (
						<Composer onSend={onSend} busy={busy} placeholder={placeholder} />
					)
				: composer}
		</div>
	);
}
