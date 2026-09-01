// @vitest-environment happy-dom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
	type ChatChunk,
	type ChatMessage,
	type ChatStreamSource,
	useChatStream,
} from "../src/chat/stream.js";

afterEach(cleanup);

// A source whose reply is a fixed sequence of deltas, resolving between each so
// the hook renders the growing placeholder step by step.
function scriptedSource(
	deltas: string[],
): ChatStreamSource & { seen: ChatMessage[][] } {
	const seen: ChatMessage[][] = [];
	return {
		seen,
		async *send(messages) {
			seen.push(messages);
			for (const delta of deltas) {
				yield { delta } satisfies ChatChunk;
			}
		},
	};
}

describe("useChatStream", () => {
	it("appends the user message and accretes the streamed reply", async () => {
		const source = scriptedSource(["Hel", "lo"]);
		const { result } = renderHook(() => useChatStream(source));

		act(() => result.current.send("hi"));

		await waitFor(() => expect(result.current.isStreaming).toBe(false));

		expect(result.current.messages.map((m) => [m.role, m.content])).toEqual([
			["user", "hi"],
			["assistant", "Hello"],
		]);
		expect(result.current.messages[1].pending).toBe(false);
		// The source sees the conversation ending in the user's message, without
		// the assistant placeholder.
		expect(source.seen[0].at(-1)).toMatchObject({
			role: "user",
			content: "hi",
		});
	});

	it("carries prior turns into the next request", async () => {
		const source = scriptedSource(["ok"]);
		const initial: ChatMessage[] = [
			{ id: "seed-user", role: "user", content: "first" },
			{ id: "seed-reply", role: "assistant", content: "earlier" },
		];
		const { result } = renderHook(() => useChatStream(source, initial));

		act(() => result.current.send("second"));
		await waitFor(() => expect(result.current.isStreaming).toBe(false));

		expect(source.seen[0].map((m) => m.content)).toEqual([
			"first",
			"earlier",
			"second",
		]);
	});

	it("trims blank input and never opens a stream", () => {
		const source = scriptedSource(["x"]);
		const { result } = renderHook(() => useChatStream(source));

		act(() => result.current.send("   "));

		expect(result.current.messages).toHaveLength(0);
		expect(source.seen).toHaveLength(0);
	});

	it("surfaces a transport error and stops streaming", async () => {
		const source: ChatStreamSource = {
			async *send() {
				await Promise.reject(new Error("stream failed"));
				yield { delta: "" };
			},
		};
		const { result } = renderHook(() => useChatStream(source));

		act(() => result.current.send("hi"));
		await waitFor(() => expect(result.current.isStreaming).toBe(false));

		expect(result.current.error?.message).toBe("stream failed");
		// The placeholder survives, no longer pending, so a partial reply stays put.
		expect(result.current.messages[1]).toMatchObject({
			role: "assistant",
			pending: false,
		});
	});

	it("assigns unique ids to every message", async () => {
		const source = scriptedSource(["a"]);
		const { result } = renderHook(() => useChatStream(source));

		act(() => result.current.send("one"));
		await waitFor(() => expect(result.current.isStreaming).toBe(false));
		act(() => result.current.send("two"));
		await waitFor(() => expect(result.current.isStreaming).toBe(false));

		const ids = result.current.messages.map((m) => m.id);
		expect(new Set(ids).size).toBe(ids.length);
	});

	it("mints ids that never collide with caller-supplied initial ids", async () => {
		const source = scriptedSource(["a"]);
		// `initial` deliberately uses the hook's own `msg-N` id scheme: a naive
		// counter starting at 0 would mint `msg-0`, duplicating the seed key and
		// fusing two messages under one React key.
		const initial: ChatMessage[] = [
			{ id: "msg-0", role: "user", content: "old q" },
			{ id: "msg-1", role: "assistant", content: "old a" },
		];
		const { result } = renderHook(() => useChatStream(source, initial));

		act(() => result.current.send("new q"));
		await waitFor(() => expect(result.current.isStreaming).toBe(false));

		const ids = result.current.messages.map((m) => m.id);
		expect(new Set(ids).size).toBe(ids.length);
		// The seeded messages are untouched and still distinct.
		expect(ids).toContain("msg-0");
		expect(ids).toContain("msg-1");
		expect(
			result.current.messages.filter((m) => m.id === "msg-0"),
		).toHaveLength(1);
	});

	it("keeps a stable send identity across streamed tokens", async () => {
		const source = scriptedSource(["a", "b", "c"]);
		const { result } = renderHook(() => useChatStream(source));
		const firstSend = result.current.send;

		act(() => result.current.send("hi"));
		await waitFor(() => expect(result.current.isStreaming).toBe(false));

		// Each delta re-rendered the hook; `send` must not have been rebuilt.
		expect(result.current.send).toBe(firstSend);
	});
});
