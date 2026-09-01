// The chat transport layer: a React hook that owns an SSE/WS stream and produces
// the `messages`/`onSend` a pure `@codefly-dev/ui/chat` `<Chat>` renders. Shipped
// from the `@codefly/saas-sdk/chat` subpath so the SDK's main entry (Connect
// facades, data-graph tooling) stays React-free.

export {
	type ChatChunk,
	type ChatMessage,
	type ChatRole,
	type ChatStreamSource,
	type UseChatStream,
	useChatStream,
} from "./stream.js";
