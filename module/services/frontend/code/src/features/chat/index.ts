// Grounded chat retrieval connector: a tool-search behind `<Chat>` that turns a
// query into a cited answer over the `Retrieval` seam, with an explicit "no
// source" refusal. A stub backend ships here; swapping in a real model or
// retriever never moves the UI.

export {
	createRetrievalConnector,
	formatRef,
	NO_SOURCE_REASON,
} from "./model/connector";
export {
	createStubConnector,
	createStubRetrieval,
	createStubSynthesizer,
	STUB_CORPUS,
} from "./model/stub";
export type {
	Chunk,
	Citation,
	Claim,
	GroundedAnswer,
	Ref,
	Retrieval,
	RetrievalConnector,
	RetrievalQuery,
	Synthesizer,
} from "./model/types";
export { Chat, type ChatProps } from "./ui/chat";
