import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createStubConnector } from "../model/stub";
import type { GroundedAnswer, RetrievalConnector } from "../model/types";
import { Chat } from "./chat";

afterEach(cleanup);

function ask(question: string) {
	fireEvent.change(screen.getByLabelText("Question"), {
		target: { value: question },
	});
	fireEvent.click(screen.getByRole("button", { name: "Ask" }));
}

describe("Chat", () => {
	it("renders a grounded, cited answer and opens a ref on click", async () => {
		const onOpenRef = vi.fn();
		render(<Chat connector={createStubConnector()} onOpenRef={onOpenRef} />);

		ask("how is tenant data isolated with row-level security?");

		const citation = await screen.findByRole("button", {
			name: "handbook@3#rls",
		});
		fireEvent.click(citation);
		expect(onOpenRef).toHaveBeenCalledWith({
			document: "handbook",
			version: "3",
			span: "rls",
		});
		const claims = [...document.querySelectorAll('[data-slot="chat-claim"]')];
		expect(
			claims.some((claim) =>
				/isolated by Postgres row-level security/i.test(
					claim.textContent ?? "",
				),
			),
		).toBe(true);
	});

	it("renders the no-source refusal state", async () => {
		render(<Chat connector={createStubConnector()} />);

		ask("quarterly revenue forecast");

		const refusal = await screen.findByText(/don't have a source/i);
		expect(refusal.getAttribute("data-slot")).toBe("chat-refusal");
	});

	it("renders identically no matter which connector produced the answer", async () => {
		const fixed: GroundedAnswer = {
			status: "answered",
			claims: [
				{
					text: "Grounded claim.",
					refs: [{ document: "doc", version: "2", span: "x" }],
				},
			],
			citations: [
				{
					ref: { document: "doc", version: "2", span: "x" },
					snippet: "Source.",
				},
			],
		};
		const connectorA: RetrievalConnector = { answer: async () => fixed };
		const connectorB: RetrievalConnector = {
			answer: async () => structuredClone(fixed),
		};

		const { container: a, unmount } = render(<Chat connector={connectorA} />);
		fireEvent.change(screen.getByLabelText("Question"), {
			target: { value: "same question" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Ask" }));
		await screen.findByText("Grounded claim.");
		const transcriptA = a.querySelector(
			'[data-slot="chat-transcript"]',
		)?.innerHTML;
		unmount();

		const { container: b } = render(<Chat connector={connectorB} />);
		fireEvent.change(screen.getByLabelText("Question"), {
			target: { value: "same question" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Ask" }));
		await screen.findByText("Grounded claim.");
		const transcriptB = b.querySelector(
			'[data-slot="chat-transcript"]',
		)?.innerHTML;

		expect(transcriptA).toBe(transcriptB);
	});
});
