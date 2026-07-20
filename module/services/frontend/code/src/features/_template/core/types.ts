// Pure domain types. No React, no Next, no TanStack imports.
//
// Everything in this file is plain TypeScript that can run in Node,
// Bun, Deno, or the browser — and can be imported by unit tests
// without any setup.

export interface ExampleEntity {
	id: string;
	name: string;
	createdAt: string;
}

export interface ExampleViewRow {
	id: string;
	display: string;
}
