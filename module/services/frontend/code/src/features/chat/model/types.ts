// Pure domain types for the grounded chat retrieval connector — the contract
// behind `<Chat>`. No React, no fetch, no browser APIs, so the whole seam is
// unit-testable in isolation and safe to import from either layer.
//
// Retrieval quality is expected to churn; this file is not. Everything the UI
// depends on lives here, and every backend behind it satisfies these same
// interfaces, so swapping a stub for a real model or retriever moves no UI.

/**
 * A citation reference into the corpus, addressable as `document@version#span`
 * (see {@link formatRef}). It is the click target behind every cited claim: the
 * document identifier, the exact version that was retrieved, and the span
 * within it. Pinning the version is what keeps a citation stable as the
 * document changes underneath it.
 */
export interface Ref {
	document: string;
	version: string;
	span: string;
}

/** A retrieved passage of source text, addressable by its {@link Ref}. */
export interface Chunk {
	ref: Ref;
	text: string;
	/** Relevance score; higher ranks first. Ordering only — never rendered. */
	score?: number;
}

/** A user question handed to the connector. */
export interface RetrievalQuery {
	text: string;
	/** Upper bound on the chunks retrieval returns. */
	limit?: number;
}

/**
 * The retrieval backend seam. `<Chat>` never touches it directly — a connector
 * composes it. A stub keyword matcher and a production vector or graph
 * retriever satisfy the same interface.
 */
export interface Retrieval {
	retrieve(query: RetrievalQuery): Promise<readonly Chunk[]>;
}

/**
 * One sentence of the answer together with the refs that support it. The
 * connector guarantees `refs` is non-empty for every claim it emits — an
 * unsupported sentence is never a claim.
 */
export interface Claim {
	text: string;
	refs: readonly Ref[];
}

/** A source surfaced under the answer, deduplicated by ref. */
export interface Citation {
	ref: Ref;
	/** The retrieved passage the ref points at. */
	snippet: string;
}

/**
 * The model seam. Given the query and the retrieved chunks it drafts claims. It
 * may only cite refs drawn from `chunks`; the connector enforces that so a
 * fabricated citation can never reach the UI.
 */
export interface Synthesizer {
	synthesize(
		query: RetrievalQuery,
		chunks: readonly Chunk[],
	): Promise<readonly Claim[]>;
}

/**
 * The connector's output: either a grounded answer whose every claim carries a
 * ref, or a refusal. There is no third state — an answer with no supporting
 * source is a refusal, not an empty answer.
 */
export type GroundedAnswer =
	| {
			status: "answered";
			claims: readonly Claim[];
			citations: readonly Citation[];
	  }
	| { status: "refused"; reason: string };

/**
 * The bound-primitive seam `<Chat>` calls: a query in, a cited answer out. It
 * is a tool-search — retrieval followed by grounded synthesis — but the UI
 * depends only on this method and {@link GroundedAnswer}, so the entire backend
 * behind it is swappable without touching the component. It always resolves to
 * a {@link GroundedAnswer}; a real implementation maps transport failures to a
 * refusal rather than rejecting.
 */
export interface RetrievalConnector {
	answer(query: RetrievalQuery): Promise<GroundedAnswer>;
}
