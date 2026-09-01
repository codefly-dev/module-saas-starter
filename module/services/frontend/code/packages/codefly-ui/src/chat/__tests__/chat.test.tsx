// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Chat } from "../chat.js";
import type { ChatMessage } from "../types.js";

afterEach(cleanup);

const transcript: ChatMessage[] = [
	{ id: "1", role: "user", content: "Hello" },
	{ id: "2", role: "assistant", content: "Hi there" },
];

describe("Chat", () => {
	it("renders every message with a role-derived author label", () => {
		render(<Chat messages={transcript} />);
		expect(screen.getByText("Hello")).toBeTruthy();
		expect(screen.getByText("Hi there")).toBeTruthy();
		expect(screen.getByText("You")).toBeTruthy();
		expect(screen.getByText("Assistant")).toBeTruthy();
	});

	it("prefers an explicit author over the role label", () => {
		render(
			<Chat
				messages={[
					{ id: "1", role: "assistant", content: "hi", author: "Robin" },
				]}
			/>,
		);
		expect(screen.getByText("Robin")).toBeTruthy();
		expect(screen.queryByText("Assistant")).toBeNull();
	});

	it("shows the empty state when there are no messages", () => {
		render(<Chat messages={[]} />);
		expect(screen.getByText("No messages yet.")).toBeTruthy();
	});

	it("renders a custom empty state when provided", () => {
		render(<Chat messages={[]} emptyState={<p>Ask me anything</p>} />);
		expect(screen.getByText("Ask me anything")).toBeTruthy();
		expect(screen.queryByText("No messages yet.")).toBeNull();
	});

	it("marks the transcript busy while a reply streams", () => {
		render(
			<Chat
				messages={[
					{ id: "1", role: "assistant", content: "typ", pending: true },
				]}
				busy
			/>,
		);
		expect(screen.getByRole("log").getAttribute("aria-busy")).toBe("true");
		expect(screen.getByLabelText("typing")).toBeTruthy();
	});

	describe("default composer", () => {
		it("submits the trimmed text and clears the input on Enter", () => {
			const onSend = vi.fn();
			render(<Chat messages={transcript} onSend={onSend} />);
			const input = screen.getByLabelText("Message") as HTMLTextAreaElement;
			fireEvent.change(input, { target: { value: "  hey  " } });
			fireEvent.keyDown(input, { key: "Enter" });
			expect(onSend).toHaveBeenCalledWith("hey");
			expect(input.value).toBe("");
		});

		it("inserts a newline on Shift+Enter instead of sending", () => {
			const onSend = vi.fn();
			render(<Chat messages={transcript} onSend={onSend} />);
			const input = screen.getByLabelText("Message");
			fireEvent.change(input, { target: { value: "line" } });
			fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
			expect(onSend).not.toHaveBeenCalled();
		});

		it("does not send empty or whitespace-only input", () => {
			const onSend = vi.fn();
			render(<Chat messages={transcript} onSend={onSend} />);
			const input = screen.getByLabelText("Message");
			fireEvent.change(input, { target: { value: "   " } });
			fireEvent.keyDown(input, { key: "Enter" });
			expect(onSend).not.toHaveBeenCalled();
		});

		it("disables send while busy", () => {
			const onSend = vi.fn();
			render(<Chat messages={transcript} onSend={onSend} busy />);
			const input = screen.getByLabelText("Message");
			fireEvent.change(input, { target: { value: "hey" } });
			fireEvent.keyDown(input, { key: "Enter" });
			expect(onSend).not.toHaveBeenCalled();
			expect(
				(screen.getByRole("button", { name: "Send" }) as HTMLButtonElement)
					.disabled,
			).toBe(true);
		});
	});

	it("renders no composer when onSend is omitted", () => {
		render(<Chat messages={transcript} />);
		expect(screen.queryByLabelText("Message")).toBeNull();
	});

	it("renders a custom composer in place of the default", () => {
		const onSend = vi.fn();
		render(
			<Chat
				messages={transcript}
				onSend={onSend}
				composer={<div>custom composer</div>}
			/>,
		);
		expect(screen.getByText("custom composer")).toBeTruthy();
		expect(screen.queryByLabelText("Message")).toBeNull();
	});
});
