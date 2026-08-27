import { createRetrievalConnector } from "./connector";
import type {
	Chunk,
	Claim,
	Retrieval,
	RetrievalConnector,
	RetrievalQuery,
	Synthesizer,
} from "./types";

const WORD = /[a-z0-9]+/g;

function terms(text: string): Set<string> {
	return new Set(text.toLowerCase().match(WORD) ?? []);
}

function overlap(query: Set<string>, chunk: Set<string>): number {
	let score = 0;
	for (const term of query) if (chunk.has(term)) score += 1;
	return score;
}

/**
 * A keyword-overlap retriever over an in-memory corpus. Deliberately trivial —
 * the point is the interface, not the ranking. A chunk sharing no term with the
 * query is never returned, so an off-topic question retrieves nothing and the
 * connector refuses.
 */
export function createStubRetrieval(corpus: readonly Chunk[]): Retrieval {
	const indexed = corpus.map((chunk) => ({ chunk, terms: terms(chunk.text) }));
	return {
		async retrieve(query: RetrievalQuery): Promise<readonly Chunk[]> {
			const wanted = terms(query.text);
			const limit = query.limit ?? 3;
			return indexed
				.map((entry) => ({
					chunk: entry.chunk,
					score: overlap(wanted, entry.terms),
				}))
				.filter((entry) => entry.score > 0)
				.sort((a, b) => b.score - a.score)
				.slice(0, limit)
				.map((entry) => ({ ...entry.chunk, score: entry.score }));
		},
	};
}

/**
 * A pass-through synthesizer: each retrieved chunk becomes one claim citing its
 * own ref. It invents no prose and therefore no ungrounded sentence — a
 * faithful stand-in for a model that must cite everything it says.
 */
export function createStubSynthesizer(): Synthesizer {
	return {
		async synthesize(
			_query: RetrievalQuery,
			chunks: readonly Chunk[],
		): Promise<readonly Claim[]> {
			return chunks.map((chunk) => ({ text: chunk.text, refs: [chunk.ref] }));
		},
	};
}

/** A small grounded corpus so the connector answers out of the box. */
export const STUB_CORPUS: readonly Chunk[] = [
	{
		ref: { document: "handbook", version: "3", span: "rls" },
		text: "Every tenant table is isolated by Postgres row-level security, so a query only ever sees rows for the caller's organization.",
	},
	{
		ref: { document: "handbook", version: "3", span: "rbac" },
		text: "Access is governed by role-based access control layered over the row-level security, with roles granted per organization.",
	},
	{
		ref: { document: "handbook", version: "3", span: "audit" },
		text: "Sensitive actions are written to an append-only audit log that records the actor, the resource, and the time of the event.",
	},
];

/** The stub tool-search: keyword retrieval over {@link STUB_CORPUS} plus the
 * pass-through synthesizer. Drop-in for `<Chat connector={…} />`. */
export function createStubConnector(
	corpus: readonly Chunk[] = STUB_CORPUS,
): RetrievalConnector {
	return createRetrievalConnector(
		createStubRetrieval(corpus),
		createStubSynthesizer(),
	);
}
