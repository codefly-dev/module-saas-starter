import { describe, expect, it } from "vitest";
import {
	createRetrievalConnector,
	formatRef,
	NO_SOURCE_REASON,
} from "../connector";
import type { Chunk, Claim, Retrieval, Synthesizer } from "../types";

const chunk = (span: string, text: string): Chunk => ({
	ref: { document: "doc", version: "1", span },
	text,
});

function retrievalOf(...chunks: Chunk[]): Retrieval {
	return { retrieve: async () => chunks };
}

function synthesizerOf(...claims: Claim[]): Synthesizer {
	return { synthesize: async () => claims };
}

describe("formatRef", () => {
	it("addresses a ref as document@version#span", () => {
		expect(formatRef({ document: "handbook", version: "3", span: "rls" })).toBe(
			"handbook@3#rls",
		);
	});
});

describe("createRetrievalConnector", () => {
	it("returns a grounded answer whose claims and citations carry refs", async () => {
		const a = chunk("a", "Alpha fact.");
		const b = chunk("b", "Beta fact.");
		const connector = createRetrievalConnector(
			retrievalOf(a, b),
			synthesizerOf(
				{ text: "Alpha fact.", refs: [a.ref] },
				{ text: "Beta fact.", refs: [b.ref] },
			),
		);

		const answer = await connector.answer({ text: "anything" });

		expect(answer.status).toBe("answered");
		if (answer.status !== "answered") return;
		expect(answer.claims.every((claim) => claim.refs.length > 0)).toBe(true);
		expect(answer.citations.map((c) => formatRef(c.ref))).toEqual([
			"doc@1#a",
			"doc@1#b",
		]);
	});

	it("refuses when retrieval returns nothing", async () => {
		const connector = createRetrievalConnector(
			retrievalOf(),
			synthesizerOf({ text: "made up", refs: [] }),
		);

		const answer = await connector.answer({ text: "off topic" });

		expect(answer).toEqual({ status: "refused", reason: NO_SOURCE_REASON });
	});

	it("drops claims that cite a ref outside the retrieved set", async () => {
		const a = chunk("a", "Grounded.");
		const connector = createRetrievalConnector(
			retrievalOf(a),
			synthesizerOf(
				{ text: "Grounded.", refs: [a.ref] },
				{
					text: "Hallucinated.",
					refs: [{ document: "doc", version: "1", span: "ghost" }],
				},
			),
		);

		const answer = await connector.answer({ text: "q" });

		expect(answer.status).toBe("answered");
		if (answer.status !== "answered") return;
		expect(answer.claims).toHaveLength(1);
		expect(answer.claims[0].text).toBe("Grounded.");
	});

	it("refuses when every drafted claim is ungrounded", async () => {
		const a = chunk("a", "Grounded.");
		const connector = createRetrievalConnector(
			retrievalOf(a),
			synthesizerOf({ text: "Unsupported.", refs: [] }),
		);

		const answer = await connector.answer({ text: "q" });

		expect(answer.status).toBe("refused");
	});

	it("deduplicates citations shared across claims", async () => {
		const a = chunk("a", "Shared source.");
		const connector = createRetrievalConnector(
			retrievalOf(a),
			synthesizerOf(
				{ text: "First.", refs: [a.ref] },
				{ text: "Second.", refs: [a.ref] },
			),
		);

		const answer = await connector.answer({ text: "q" });

		expect(answer.status).toBe("answered");
		if (answer.status !== "answered") return;
		expect(answer.citations).toHaveLength(1);
		expect(answer.citations[0].snippet).toBe("Shared source.");
	});
});
