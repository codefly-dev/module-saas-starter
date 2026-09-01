import { useCallback, useEffect, useRef, useState } from "react";

// The streaming half of the chat kit. `@codefly-dev/ui`'s `<Chat>` is pure
// presentation; this hook owns the transport and state, feeding it `messages`
// and `onSend` — the same split as `runDashboard` (data) → `<Dashboard>` (view).

/** Who authored a message. Mirrors `@codefly-dev/ui/chat`'s `ChatRole`. */
export type ChatRole = "user" | "assistant" | "system";

/** One message in the transcript. Structurally matches `@codefly-dev/ui`'s `ChatMessage`. */
export interface ChatMessage {
	id: string;
	role: ChatRole;
	content: string;
	author?: string;
	/** The assistant reply still streaming in. */
	pending?: boolean;
}

/** One incremental piece of an assistant reply. */
export interface ChatChunk {
	delta: string;
}

/**
 * The transport contract the hook owns. Given the conversation so far, a source
 * streams the assistant's reply as content deltas. An SSE reader and a WebSocket
 * client both satisfy this structurally — the hook never knows which, exactly as
 * `runDashboard` takes any `AuditAggregateClient`.
 */
export interface ChatStreamSource {
	send(
		messages: ChatMessage[],
		options: { signal: AbortSignal },
	): AsyncIterable<ChatChunk>;
}

export interface UseChatStream {
	messages: ChatMessage[];
	/** Append a user message and stream the assistant's reply. */
	send: (content: string) => void;
	/** A reply is streaming; the composer disables its send while true. */
	isStreaming: boolean;
	/** The transport error that ended the last stream, if any. */
	error?: Error;
}

/**
 * Drive a streaming chat against a `ChatStreamSource`. Wire the result straight
 * into `@codefly-dev/ui/chat`'s `<Chat>`:
 *
 * ```tsx
 * const { messages, send, isStreaming } = useChatStream(source);
 * return <Chat messages={messages} onSend={send} busy={isStreaming} />;
 * ```
 *
 * The user's message and a placeholder assistant message are appended on `send`;
 * incoming deltas accrete into the placeholder until the stream ends.
 */
export function useChatStream(
	source: ChatStreamSource,
	initial: ChatMessage[] = [],
): UseChatStream {
	const [messages, setMessages] = useState<ChatMessage[]>(initial);
	const [isStreaming, setIsStreaming] = useState(false);
	const [error, setError] = useState<Error | undefined>();

	// Latch the latest messages and source so `send` is a stable identity with no
	// dependencies — otherwise it would be rebuilt on every streamed token (each
	// delta changes `messages`), defeating a consumer that memoizes on `onSend`.
	// The refs sync in an effect (a render-phase ref write is disallowed); `send`
	// only fires from events, which run after the effect has committed.
	const messagesRef = useRef(messages);
	const sourceRef = useRef(source);
	useEffect(() => {
		messagesRef.current = messages;
		sourceRef.current = source;
	});

	// Every id ever placed in the transcript. A minted id is checked against it so
	// it can never collide with a caller-supplied `initial` id (which need not
	// follow the `msg-N` scheme) or a prior minted id — a duplicate React key
	// silently fuses two messages into one and corrupts the transcript.
	const usedIds = useRef<Set<string>>(new Set(initial.map((m) => m.id)));
	const nextId = useRef(0);
	const abort = useRef<AbortController | null>(null);
	const streaming = useRef(false);

	const mintId = useCallback((): string => {
		let candidate: string;
		do {
			candidate = `msg-${nextId.current++}`;
		} while (usedIds.current.has(candidate));
		usedIds.current.add(candidate);
		return candidate;
	}, []);

	const send = useCallback(
		(content: string) => {
			// One stream at a time: the composer is disabled while streaming, but a
			// custom composer could still call through — ignore rather than
			// interleave two replies into the same placeholder.
			if (streaming.current) return;
			const text = content.trim();
			if (text === "") return;

			const userMessage: ChatMessage = {
				id: mintId(),
				role: "user",
				content: text,
			};
			const replyId = mintId();
			const conversation = [...messagesRef.current, userMessage];

			setMessages([
				...conversation,
				{ id: replyId, role: "assistant", content: "", pending: true },
			]);
			setError(undefined);
			setIsStreaming(true);
			streaming.current = true;

			const controller = new AbortController();
			abort.current = controller;

			void (async () => {
				try {
					for await (const chunk of sourceRef.current.send(conversation, {
						signal: controller.signal,
					})) {
						setMessages((current) =>
							current.map((message) =>
								message.id === replyId
									? { ...message, content: message.content + chunk.delta }
									: message,
							),
						);
					}
				} catch (cause) {
					// An abort is a caller-initiated teardown (unmount), not a failure:
					// the placeholder is already gone, so surface nothing.
					if (controller.signal.aborted) return;
					setError(cause instanceof Error ? cause : new Error(String(cause)));
				} finally {
					if (!controller.signal.aborted) {
						setMessages((current) =>
							current.map((message) =>
								message.id === replyId
									? { ...message, pending: false }
									: message,
							),
						);
						setIsStreaming(false);
						streaming.current = false;
						abort.current = null;
					}
				}
			})();
		},
		[mintId],
	);

	// Abort an in-flight stream when the component unmounts so a resolved delta
	// never calls `setMessages` on an unmounted tree.
	useEffect(() => () => abort.current?.abort(), []);

	return { messages, send, isStreaming, error };
}
