import type { ReferenceSummary } from "./model.js";

export function presentReferenceSummary(active: boolean): ReferenceSummary {
	return { title: "Reference contribution", active };
}
