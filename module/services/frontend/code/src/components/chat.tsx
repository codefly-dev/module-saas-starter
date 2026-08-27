"use client";

/**
 * Chat — a generic streaming chat surface bound to a backend-declared
 * connector.
 *
 * Where `<Dashboard>` is bound to `data`, `<Chat>` is bound to a
 * `connector` (`<Chat connector={…} />`): the transport behind the seam.
 * The component owns the presentation — the message list, the composer,
 * streaming, stop / regenerate, tool cards, refusals, and — first-class —
 * the Ref→click-through citation renderer. It knows nothing about how bytes
 * reach the model; that is the connector's job.
 *
 * Interface-first: the deliverable is the `ChatConnector` contract plus the
 * citation rendering. `createStubChatConnector` is a placeholder transport
 * that satisfies the contract until a real backend lands, so the seam is
 * concrete and demonstrable today.
 */

import { Ban, Check, Loader2, RefreshCw, Send, Square, X } from "lucide-react";
import {
	type KeyboardEvent,
	type ReactNode,
	useCallback,
	useRef,
	useState,
} from "react";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

// ── Connector contract — the transport behind the seam ──────────────────────

/**
 * A source a message can cite. The renderer turns `[n]` markers in the
 * message text (1-based, into `ChatMessage.citations`) into click-through
 * refs and lists the sources beneath the message.
 */
export interface Citation {
	/** Human title of the source, shown in the reference list and on hover. */
	title: string;
	/** Click-through target. When set, refs render as real links. */
	href?: string;
	/** Short preview shown under the title in the reference list. */
	snippet?: string;
	/** Overrides the numeric marker (e.g. a short code) when set. */
	label?: string;
}

/** A tool the assistant invoked, rendered as a card in the message stream. */
export interface ToolInvocation {
	/** Stable id — later stream events with the same id update this card. */
	id: string;
	name: string;
	status: "pending" | "running" | "success" | "error";
	/** One-line summary shown under the tool name. */
	detail?: string;
}

/** The ordered pieces an assistant message is composed of. */
export type MessagePart =
	| { type: "text"; text: string }
	| { type: "tool"; tool: ToolInvocation }
	| { type: "refusal"; reason: string };

export interface ChatMessage {
	id: string;
	role: "user" | "assistant";
	parts: MessagePart[];
	/** Sources referenced by `[n]` markers in this message's text. */
	citations: Citation[];
}

/** A delta the connector streams while composing one assistant reply. */
export type ChatStreamEvent =
	| { type: "text"; text: string }
	| { type: "tool"; tool: ToolInvocation }
	| { type: "citation"; citation: Citation }
	| { type: "refusal"; reason: string };

export interface ChatConnector {
	/**
	 * Stream the assistant's reply to `messages` (the conversation so far,
	 * ending with the user turn). Implementations MUST stop promptly when
	 * `signal` aborts — that is the stop button.
	 */
	send(
		messages: ChatMessage[],
		options: { signal: AbortSignal },
	): AsyncIterable<ChatStreamEvent>;
}

// ── Ref→click-through citation renderer ─────────────────────────────────────

function Ref({
	n,
	citation,
	onClick,
}: {
	n: number;
	citation: Citation;
	onClick?: (citation: Citation) => void;
}) {
	const className = cn(
		"mx-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded",
		"px-1 align-super text-[0.65rem] font-medium leading-none",
		"bg-primary/10 text-primary hover:bg-primary/20",
		"focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
	);
	const label = citation.label ?? String(n);
	// A href makes the ref a real, keyboard- and right-clickable link;
	// onClick still fires so callers can react (scroll, analytics, panel).
	if (citation.href) {
		return (
			<a
				href={citation.href}
				target="_blank"
				rel="noreferrer"
				title={citation.title}
				className={className}
				onClick={() => onClick?.(citation)}
			>
				{label}
			</a>
		);
	}
	return (
		<button
			type="button"
			title={citation.title}
			className={className}
			onClick={() => onClick?.(citation)}
		>
			{label}
		</button>
	);
}

/**
 * Renders text with `[n]` markers replaced by click-through refs. Markers
 * that fall outside the citation range are left as literal text, so ordinary
 * bracketed numbers (array indices, versions) survive untouched.
 */
export function CitedText({
	text,
	citations,
	onCitationClick,
}: {
	text: string;
	citations: Citation[];
	onCitationClick?: (citation: Citation) => void;
}): ReactNode {
	if (citations.length === 0) return text;

	const nodes: ReactNode[] = [];
	let cursor = 0;
	let key = 0;
	for (const match of text.matchAll(/\[(\d+)\]/g)) {
		const citation = citations[Number(match[1]) - 1];
		if (!citation) continue;
		if (match.index > cursor) nodes.push(text.slice(cursor, match.index));
		nodes.push(
			<Ref
				key={key++}
				n={Number(match[1])}
				citation={citation}
				onClick={onCitationClick}
			/>,
		);
		cursor = match.index + match[0].length;
	}
	if (cursor < text.length) nodes.push(text.slice(cursor));
	return nodes;
}

function References({
	citations,
	onCitationClick,
}: {
	citations: Citation[];
	onCitationClick?: (citation: Citation) => void;
}) {
	return (
		<ol className="mt-2 space-y-1 border-t pt-2 text-xs text-muted-foreground">
			{citations.map((citation, i) => {
				const content = (
					<>
						<span className="font-medium text-foreground">{i + 1}.</span>{" "}
						<span className="text-foreground">{citation.title}</span>
						{citation.snippet && (
							<span className="text-muted-foreground">
								{" "}
								— {citation.snippet}
							</span>
						)}
					</>
				);
				return (
					<li key={`${i}-${citation.title}`}>
						{citation.href ? (
							<a
								href={citation.href}
								target="_blank"
								rel="noreferrer"
								className="hover:underline"
								onClick={() => onCitationClick?.(citation)}
							>
								{content}
							</a>
						) : (
							<button
								type="button"
								className="text-left hover:underline"
								onClick={() => onCitationClick?.(citation)}
							>
								{content}
							</button>
						)}
					</li>
				);
			})}
		</ol>
	);
}

// ── Message parts ───────────────────────────────────────────────────────────

const toolStatusIcon: Record<ToolInvocation["status"], ReactNode> = {
	pending: (
		<Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
	),
	running: <Loader2 className="h-3.5 w-3.5 animate-spin text-primary" />,
	success: <Check className="h-3.5 w-3.5 text-primary" />,
	error: <X className="h-3.5 w-3.5 text-destructive" />,
};

function ToolCard({ tool }: { tool: ToolInvocation }) {
	return (
		<div className="my-1.5 flex items-start gap-2 rounded-lg border bg-muted/40 px-3 py-2 text-sm">
			<span className="mt-0.5">{toolStatusIcon[tool.status]}</span>
			<div className="min-w-0">
				<span className="font-mono text-xs font-medium">{tool.name}</span>
				{tool.detail && (
					<p className="text-xs text-muted-foreground">{tool.detail}</p>
				)}
			</div>
		</div>
	);
}

function Refusal({ reason }: { reason: string }) {
	return (
		<div className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
			<Ban className="mt-0.5 h-4 w-4 shrink-0" />
			<p>{reason}</p>
		</div>
	);
}

function MessageParts({
	message,
	onCitationClick,
}: {
	message: ChatMessage;
	onCitationClick?: (citation: Citation) => void;
}) {
	return (
		<>
			{message.parts.map((part, i) => {
				switch (part.type) {
					case "text":
						return (
							<p key={i} className="whitespace-pre-wrap leading-relaxed">
								<CitedText
									text={part.text}
									citations={message.citations}
									onCitationClick={onCitationClick}
								/>
							</p>
						);
					case "tool":
						return <ToolCard key={i} tool={part.tool} />;
					case "refusal":
						return <Refusal key={i} reason={part.reason} />;
					default: {
						// A new part kind must be handled here or this fails to type-check.
						const _exhaustive: never = part;
						return _exhaustive;
					}
				}
			})}
		</>
	);
}

function MessageBubble({
	message,
	onCitationClick,
}: {
	message: ChatMessage;
	onCitationClick?: (citation: Citation) => void;
}) {
	const isUser = message.role === "user";
	return (
		<div className={cn("flex gap-3", isUser && "flex-row-reverse")}>
			<Avatar className="h-7 w-7 shrink-0">
				<AvatarFallback className="text-xs">
					{isUser ? "You" : "AI"}
				</AvatarFallback>
			</Avatar>
			<div
				className={cn(
					"max-w-[80%] rounded-2xl px-3.5 py-2 text-sm",
					isUser
						? "bg-primary text-primary-foreground"
						: "bg-muted text-foreground",
				)}
			>
				<MessageParts message={message} onCitationClick={onCitationClick} />
				{!isUser && message.citations.length > 0 && (
					<References
						citations={message.citations}
						onCitationClick={onCitationClick}
					/>
				)}
			</div>
		</div>
	);
}

// ── State ───────────────────────────────────────────────────────────────────

let idSeq = 0;
const nextId = () => `m${++idSeq}`;

function applyEvent(message: ChatMessage, event: ChatStreamEvent): ChatMessage {
	const parts = message.parts.slice();
	switch (event.type) {
		case "text": {
			const last = parts[parts.length - 1];
			if (last?.type === "text") {
				parts[parts.length - 1] = {
					type: "text",
					text: last.text + event.text,
				};
			} else {
				parts.push({ type: "text", text: event.text });
			}
			return { ...message, parts };
		}
		case "tool": {
			const at = parts.findIndex(
				(p) => p.type === "tool" && p.tool.id === event.tool.id,
			);
			if (at >= 0) parts[at] = { type: "tool", tool: event.tool };
			else parts.push({ type: "tool", tool: event.tool });
			return { ...message, parts };
		}
		case "refusal":
			parts.push({ type: "refusal", reason: event.reason });
			return { ...message, parts };
		case "citation":
			return { ...message, citations: [...message.citations, event.citation] };
		default: {
			const _exhaustive: never = event;
			return _exhaustive;
		}
	}
}

function findLastIndex<T>(items: T[], predicate: (item: T) => boolean): number {
	for (let i = items.length - 1; i >= 0; i--) {
		if (predicate(items[i])) return i;
	}
	return -1;
}

function useChat(connector: ChatConnector) {
	const [messages, setMessages] = useState<ChatMessage[]>([]);
	const [status, setStatus] = useState<"idle" | "streaming">("idle");
	const [error, setError] = useState<unknown>(null);
	const controllerRef = useRef<AbortController | null>(null);

	const run = useCallback(
		async (history: ChatMessage[]) => {
			const controller = new AbortController();
			controllerRef.current = controller;
			setStatus("streaming");
			setError(null);

			let assistant: ChatMessage = {
				id: nextId(),
				role: "assistant",
				parts: [],
				citations: [],
			};
			setMessages([...history, assistant]);

			try {
				for await (const event of connector.send(history, {
					signal: controller.signal,
				})) {
					assistant = applyEvent(assistant, event);
					const snapshot = assistant;
					setMessages((prev) => [...prev.slice(0, -1), snapshot]);
				}
			} catch (err) {
				// Aborting via the stop button is not an error — keep the partial reply.
				if (!controller.signal.aborted) setError(err);
			} finally {
				controllerRef.current = null;
				setStatus("idle");
			}
		},
		[connector],
	);

	const send = useCallback(
		(text: string) => {
			const user: ChatMessage = {
				id: nextId(),
				role: "user",
				parts: [{ type: "text", text }],
				citations: [],
			};
			setMessages((prev) => {
				run([...prev, user]);
				return prev;
			});
		},
		[run],
	);

	const stop = useCallback(() => controllerRef.current?.abort(), []);

	const regenerate = useCallback(() => {
		setMessages((prev) => {
			const lastUser = findLastIndex(prev, (m) => m.role === "user");
			if (lastUser >= 0) run(prev.slice(0, lastUser + 1));
			return prev;
		});
	}, [run]);

	return { messages, status, error, send, stop, regenerate };
}

// ── Chat ────────────────────────────────────────────────────────────────────

export interface ChatProps {
	connector: ChatConnector;
	/** Fired when a citation ref or reference-list entry is clicked. */
	onCitationClick?: (citation: Citation) => void;
	placeholder?: string;
	/** Shown before the first message is sent. */
	emptyState?: ReactNode;
	className?: string;
}

export function Chat({
	connector,
	onCitationClick,
	placeholder = "Send a message…",
	emptyState,
	className,
}: ChatProps) {
	const { messages, status, error, send, stop, regenerate } =
		useChat(connector);
	const [draft, setDraft] = useState("");
	const isStreaming = status === "streaming";

	const submit = () => {
		const text = draft.trim();
		if (!text || isStreaming) return;
		setDraft("");
		send(text);
	};

	const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
		if (e.key === "Enter" && !e.shiftKey) {
			e.preventDefault();
			submit();
		}
	};

	const canRegenerate =
		!isStreaming && messages[messages.length - 1]?.role === "assistant";

	return (
		<div className={cn("flex h-full flex-col", className)}>
			<div
				role="log"
				aria-live="polite"
				className="flex-1 space-y-4 overflow-y-auto p-4"
			>
				{messages.length === 0
					? emptyState
					: messages.map((message) => (
							<MessageBubble
								key={message.id}
								message={message}
								onCitationClick={onCitationClick}
							/>
						))}
				{error != null && (
					<p className="text-sm text-destructive" role="alert">
						Something went wrong. Try again.
					</p>
				)}
			</div>

			<div className="border-t p-3">
				{canRegenerate && (
					<div className="mb-2">
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={regenerate}
						>
							<RefreshCw className="mr-1.5 h-3.5 w-3.5" />
							Regenerate
						</Button>
					</div>
				)}
				<div className="flex items-end gap-2">
					<Textarea
						value={draft}
						onChange={(e) => setDraft(e.target.value)}
						onKeyDown={onKeyDown}
						placeholder={placeholder}
						rows={1}
						className="min-h-9 resize-none"
					/>
					{isStreaming ? (
						<Button
							type="button"
							variant="outline"
							size="icon"
							onClick={stop}
							aria-label="Stop"
						>
							<Square className="h-4 w-4" />
						</Button>
					) : (
						<Button
							type="button"
							size="icon"
							onClick={submit}
							disabled={draft.trim().length === 0}
							aria-label="Send"
						>
							<Send className="h-4 w-4" />
						</Button>
					)}
				</div>
			</div>
		</div>
	);
}

// ── Stub connector ──────────────────────────────────────────────────────────

const delay = (ms: number, signal: AbortSignal) =>
	new Promise<void>((resolve, reject) => {
		if (signal.aborted) return reject(signal.reason);
		const timer = setTimeout(resolve, ms);
		signal.addEventListener(
			"abort",
			() => {
				clearTimeout(timer);
				reject(signal.reason);
			},
			{ once: true },
		);
	});

function lastUserText(messages: ChatMessage[]): string {
	const user = messages.findLast?.((m) => m.role === "user");
	const part = user?.parts.find((p) => p.type === "text");
	return part?.type === "text" ? part.text : "";
}

/**
 * A placeholder transport that satisfies `ChatConnector` until a real backend
 * lands. It streams a canned reply exercising the full part vocabulary — text
 * with citation refs, a tool card, sources — and refuses when the prompt asks
 * it to, so the seam and every render path are demonstrable today.
 */
export function createStubChatConnector({
	delayMs = 60,
}: {
	delayMs?: number;
} = {}): ChatConnector {
	return {
		async *send(messages, { signal }) {
			const prompt = lastUserText(messages).toLowerCase();

			if (prompt.includes("refuse")) {
				await delay(delayMs, signal);
				yield {
					type: "refusal",
					reason: "I can't help with that request.",
				};
				return;
			}

			yield {
				type: "tool",
				tool: { id: "search", name: "search_docs", status: "running" },
			};
			await delay(delayMs, signal);
			yield {
				type: "tool",
				tool: {
					id: "search",
					name: "search_docs",
					status: "success",
					detail: "2 sources found",
				},
			};

			yield {
				type: "citation",
				citation: {
					title: "Connector guide",
					href: "#connector",
					snippet: "How connectors bind to the chat surface.",
				},
			};
			yield {
				type: "citation",
				citation: {
					title: "Citation renderer",
					href: "#citations",
					snippet: "Refs render as click-through markers.",
				},
			};

			for (const chunk of [
				"Connectors are the transport behind the chat seam [1]. ",
				"Citations render inline as click-through refs [2].",
			]) {
				await delay(delayMs, signal);
				yield { type: "text", text: chunk };
			}
		},
	};
}
