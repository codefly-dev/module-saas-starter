import type {
	Chunk,
	Citation,
	Claim,
	GroundedAnswer,
	Ref,
	Retrieval,
	RetrievalConnector,
	RetrievalQuery,
	Synthesizer,
} from "./types";

/** Render a ref as its canonical `document@version#span` address. */
export function formatRef(ref: Ref): string {
	return `${ref.document}@${ref.version}#${ref.span}`;
}

/** Refusal shown when nothing grounds an answer. */
export const NO_SOURCE_REASON = "I don't have a source for that.";

/**
 * Compose a retrieval backend and a synthesizer into the tool-search behind
 * `<Chat>`. The connector owns the grounding contract the component then
 * trusts:
 *
 *  - every emitted claim cites at least one ref, and every cited ref resolves
 *    to a chunk retrieval actually returned — a claim citing anything else is
 *    dropped;
 *  - if nothing was retrieved, or synthesis leaves no grounded claim, the
 *    result is a refusal rather than an unsupported answer.
 *
 * Because those guarantees live here and not in the UI, swapping either seam
 * for a real implementation cannot change what the component has to render.
 */
export function createRetrievalConnector(
	retrieval: Retrieval,
	synthesizer: Synthesizer,
): RetrievalConnector {
	async function answer(query: RetrievalQuery): Promise<GroundedAnswer> {
		const chunks = await retrieval.retrieve(query);
		if (chunks.length === 0) {
			return { status: "refused", reason: NO_SOURCE_REASON };
		}

		const byRef = new Map<string, Chunk>();
		for (const chunk of chunks) byRef.set(formatRef(chunk.ref), chunk);

		const drafted = await synthesizer.synthesize(query, chunks);
		const claims: Claim[] = [];
		for (const claim of drafted) {
			const refs = claim.refs.filter((ref) => byRef.has(formatRef(ref)));
			if (refs.length > 0) claims.push({ text: claim.text, refs });
		}
		if (claims.length === 0) {
			return { status: "refused", reason: NO_SOURCE_REASON };
		}

		const citations: Citation[] = [];
		const seen = new Set<string>();
		for (const claim of claims) {
			for (const ref of claim.refs) {
				const key = formatRef(ref);
				if (seen.has(key)) continue;
				seen.add(key);
				citations.push({ ref, snippet: byRef.get(key)!.text });
			}
		}

		return { status: "answered", claims, citations };
	}

	return { answer };
}
