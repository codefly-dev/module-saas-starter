// The board tier: one collection in columns, one column per value of a field,
// whose cards a reader drags — or moves from a menu — between them. Exported
// from `@codefly-dev/ui/board` (a client subpath), shared as a
// Module-Federation singleton like every other kit subpath.
//
// The board commits nothing: a move is reported to the consumer, which confirms
// it, writes it, or refuses it. See board.tsx.

export { Board, type BoardColumn, type BoardProps } from "./board.js";
