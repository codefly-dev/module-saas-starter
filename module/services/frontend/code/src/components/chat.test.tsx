import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	Chat,
	type ChatConnector,
	type ChatStreamEvent,
	type Citation,
	CitedText,
	createStubChatConnector,
} from "./chat";

afterEach(cleanup);

function fromEvents(...events: ChatStreamEvent[]): ChatConnector {
	return {
		async *send() {
			for (const event of events) yield event;
		},
	};
}

function typeAndSend(text: string) {
	fireEvent.change(screen.getByPlaceholderText("Send a message…"), {
		target: { value: text },
	});
	fireEvent.click(screen.getByLabelText("Send"));
}

describe("CitedText — Ref→click-through citation renderer", () => {
	const citations: Citation[] = [
		{ title: "First source", href: "https://example.com/1" },
		{ title: "Second source" },
	];

	it("renders `[n]` markers as click-through refs", () => {
		render(
			<div>
				<CitedText text="See [1] and [2]." citations={citations} />
			</div>,
		);
		// [1] has an href → a real link; [2] has none → a button.
		const link = screen.getByRole("link", { name: "1" });
		expect(link.getAttribute("href")).toBe("https://example.com/1");
		expect(screen.getByRole("button", { name: "2" })).toBeTruthy();
	});

	it("leaves out-of-range markers as literal text", () => {
		render(
			<div>
				<CitedText
					text="Index [9] survives, [1] does not."
					citations={citations}
				/>
			</div>,
		);
		expect(screen.getByText(/\[9\]/)).toBeTruthy();
		expect(screen.getByRole("link", { name: "1" })).toBeTruthy();
	});

	it("invokes onCitationClick when a ref is clicked", () => {
		const onCitationClick = vi.fn();
		render(
			<div>
				<CitedText
					text="Ref [2]."
					citations={citations}
					onCitationClick={onCitationClick}
				/>
			</div>,
		);
		fireEvent.click(screen.getByRole("button", { name: "2" }));
		expect(onCitationClick).toHaveBeenCalledWith(citations[1]);
	});
});

describe("Chat", () => {
	it("streams a reply with tool cards and cited sources", async () => {
		render(<Chat connector={createStubChatConnector({ delayMs: 0 })} />);
		typeAndSend("hello");

		expect(await screen.findByText("search_docs")).toBeTruthy();
		expect(await screen.findByText("2 sources found")).toBeTruthy();
		expect(
			await screen.findByText(/Connectors are the transport/),
		).toBeTruthy();
		// Cited sources are listed and the inline refs are click-through.
		expect(await screen.findByText("Connector guide")).toBeTruthy();
		expect(await screen.findByRole("link", { name: "1" })).toBeTruthy();
	});

	it("renders a refusal state", async () => {
		render(<Chat connector={createStubChatConnector({ delayMs: 0 })} />);
		typeAndSend("please refuse this");
		expect(
			await screen.findByText("I can't help with that request."),
		).toBeTruthy();
	});

	it("stops streaming, keeping the partial reply", async () => {
		// Streams one chunk then blocks until the abort signal fires.
		const connector: ChatConnector = {
			async *send(_messages, { signal }) {
				yield { type: "text", text: "partial answer" };
				await new Promise<void>((_resolve, reject) => {
					signal.addEventListener("abort", () => reject(signal.reason), {
						once: true,
					});
				});
			},
		};
		render(<Chat connector={connector} />);
		typeAndSend("go");

		expect(await screen.findByText("partial answer")).toBeTruthy();
		fireEvent.click(screen.getByLabelText("Stop"));

		// Stop resolves to idle (Send returns) and the partial reply survives.
		expect(await screen.findByLabelText("Send")).toBeTruthy();
		expect(screen.getByText("partial answer")).toBeTruthy();
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("regenerates from the last user turn", async () => {
		let calls = 0;
		const connector: ChatConnector = {
			async *send() {
				calls += 1;
				yield { type: "text", text: `reply ${calls}` };
			},
		};
		render(<Chat connector={connector} />);
		typeAndSend("hi");
		expect(await screen.findByText("reply 1")).toBeTruthy();

		fireEvent.click(screen.getByRole("button", { name: /Regenerate/ }));
		expect(await screen.findByText("reply 2")).toBeTruthy();
		expect(screen.queryByText("reply 1")).toBeNull();
	});

	it("surfaces a connector error", async () => {
		const connector: ChatConnector = {
			async *send() {
				throw new Error("transport down");
			},
		};
		render(<Chat connector={connector} />);
		typeAndSend("hi");
		expect(await screen.findByRole("alert")).toBeTruthy();
	});

	it("shows the empty state before the first message", () => {
		render(
			<Chat connector={fromEvents()} emptyState={<p>Ask me anything</p>} />,
		);
		expect(screen.getByText("Ask me anything")).toBeTruthy();
	});
});
