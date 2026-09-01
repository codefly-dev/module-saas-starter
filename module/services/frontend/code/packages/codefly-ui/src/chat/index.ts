// The shared chat kit: a pure, data-in <Chat> composite built from tokens-driven
// primitives. Exported from `@codefly-dev/ui/chat` (a client subpath) so the host app
// and solution remotes render chat from one shared package instance. Pair with
// `@codefly/saas-sdk`'s `useChatStream` for streaming transport: it owns the
// SSE/WS and feeds `messages`/`onSend` in, the same split as `runDashboard` and
// `<Dashboard>`.

export { Chat, type ChatProps } from "./chat.js";
export type { ChatMessage, ChatRole } from "./types.js";
