import type { ExampleEntity, ExampleViewRow } from "./types";

// Pure view-model shaper. This is exactly the kind of function that
// carries ~80% of feature logic and is trivially unit-tested.
export function toExampleViewRows(entities: ExampleEntity[]): ExampleViewRow[] {
	return entities
		.slice()
		.sort((a, b) => a.createdAt.localeCompare(b.createdAt))
		.map((e) => ({
			id: e.id,
			display: `${e.name} (${e.createdAt.slice(0, 10)})`,
		}));
}
