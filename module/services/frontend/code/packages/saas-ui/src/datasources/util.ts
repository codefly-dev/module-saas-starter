// A tiny classnames joiner, inlined so the kit carries no dependency (truthy
// joining only, no tailwind-merge conflict resolution).
export function cn(...parts: Array<string | false | null | undefined>): string {
	return parts.filter(Boolean).join(" ");
}

/**
 * Split the free-text paths field into the repeated `paths` the RPC expects.
 * Blank lines and stray commas are dropped, so an empty field means "the whole
 * repo" rather than a single empty prefix.
 */
export function parsePaths(raw: string | undefined): string[] {
	if (!raw) return [];
	return raw
		.split(/[\n,]/)
		.map((path) => path.trim())
		.filter(Boolean);
}

export function formatSyncedAt(iso: string | undefined): string {
	if (!iso) return "Never";
	const date = new Date(iso);
	return Number.isNaN(date.getTime()) ? "Never" : date.toLocaleDateString();
}

/**
 * Boundary node ids are UUIDs; the leading group is enough to tell two
 * boundaries apart in a table cell when no name could be resolved.
 */
export function shortBoundaryId(nodeId: string): string {
	return nodeId.split("-")[0] || nodeId;
}

/** "read" + "write" -> "Read · Write", in a stable order. */
export function formatGrants(actions: string[]): string {
	const order = ["read", "write"];
	return [...actions]
		.sort((a, b) => order.indexOf(a) - order.indexOf(b))
		.map((action) => action.charAt(0).toUpperCase() + action.slice(1))
		.join(" · ");
}
