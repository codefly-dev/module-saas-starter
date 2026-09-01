// Presentational view model for the shared <Chat> renderer. Like the dashboard
// kit, it is decoupled from any transport: the renderer takes an *already
// resolved* message list, so it never opens a socket, never reaches for host
// hooks, and can run identically in the host app and in a solution's
// Module-Federation remote.
//
// `@codefly/saas-sdk`'s `useChatStream` produces exactly these shapes — it owns
// the SSE/WS transport and hands `messages`/`onSend` down — so this view stays a
// pure component with no dependency on the SDK's transport stack.

/** Who authored a message. */
export type ChatRole = "user" | "assistant" | "system";

/** One message in the transcript. */
export interface ChatMessage {
	id: string;
	role: ChatRole;
	content: string;
	/** Display name for the author; falls back to a role-derived label. */
	author?: string;
	/** The assistant reply still streaming in — renders a live typing caret. */
	pending?: boolean;
}
