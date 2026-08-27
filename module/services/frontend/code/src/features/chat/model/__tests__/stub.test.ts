import { describe, expect, it } from "vitest";
import { formatRef } from "../connector";
import { createStubConnector, createStubRetrieval, STUB_CORPUS } from "../stub";

describe("createStubRetrieval", () => {
	it("returns only chunks sharing a term with the query, ranked by overlap", async () => {
		const retrieval = createStubRetrieval(STUB_CORPUS);

		const chunks = await retrieval.retrieve({ text: "row-level security" });

		expect(chunks.length).toBeGreaterThan(0);
		expect(formatRef(chunks[0].ref)).toBe("handbook@3#rls");
	});

	it("returns nothing for an off-topic query", async () => {
		const retrieval = createStubRetrieval(STUB_CORPUS);

		expect(
			await retrieval.retrieve({ text: "quarterly revenue forecast" }),
		).toEqual([]);
	});

	it("honours the limit", async () => {
		const retrieval = createStubRetrieval(STUB_CORPUS);

		const chunks = await retrieval.retrieve({
			text: "organization row audit access",
			limit: 1,
		});

		expect(chunks).toHaveLength(1);
	});
});

describe("createStubConnector", () => {
	it("answers a grounded question with cited claims", async () => {
		const answer = await createStubConnector().answer({
			text: "how is tenant data isolated?",
		});

		expect(answer.status).toBe("answered");
		if (answer.status !== "answered") return;
		expect(answer.claims.length).toBeGreaterThan(0);
		expect(answer.claims.every((claim) => claim.refs.length > 0)).toBe(true);
	});

	it("refuses when the corpus has no source", async () => {
		const answer = await createStubConnector().answer({
			text: "quarterly revenue forecast",
		});

		expect(answer.status).toBe("refused");
	});
});
