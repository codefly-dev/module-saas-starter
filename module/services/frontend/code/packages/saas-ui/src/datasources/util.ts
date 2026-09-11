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

function parseInstant(iso: string | undefined): Date | undefined {
	if (!iso) return undefined;
	const date = new Date(iso);
	return Number.isNaN(date.getTime()) ? undefined : date;
}

export function formatSyncedAt(iso: string | undefined): string {
	return parseInstant(iso)?.toLocaleString() ?? "Never";
}

/**
 * The ingest line: when the change-set compiler last durably enqueued a change
 * set, and the head commit it covered.
 *
 * Labelled by what moved, not by what triggered it. Three paths advance this
 * clock — a webhook delivery, the periodic reconcile, and a tenant's "Sync
 * now", which for a github source dispatches a forced reconcile rather than a
 * pull — so naming any one of them here would misattribute the other two.
 *
 * Carries the time, not just the date: the clock moves on every delivery, so a
 * date alone cannot separate a source that ingested minutes ago from one whose
 * ingest stopped shortly after midnight.
 */
export function formatIngest(
	iso: string | undefined,
	commit: string | undefined,
): string | undefined {
	const at = parseInstant(iso);
	if (!at) return undefined;
	const when = at.toLocaleString(undefined, {
		dateStyle: "short",
		timeStyle: "short",
	});
	return commit
		? `last ingest ${when} · ${commit.slice(0, 7)}`
		: `last ingest ${when}`;
}

/**
 * Boundary node ids are UUIDs; the leading group is enough to tell two
 * boundaries apart in a table cell when no name could be resolved.
 */
export function shortBoundaryId(nodeId: string): string {
	return nodeId.split("-")[0] || nodeId;
}

/**
 * "read" + "write" -> "Read · Write", in a stable order. An action outside the
 * known order sorts last rather than first: `indexOf` returns -1 for it, which
 * would otherwise rank an unrecognized grant ahead of `read`.
 */
export function formatGrants(actions: string[]): string {
	const order = ["read", "write"];
	const rank = (action: string) => {
		const at = order.indexOf(action);
		return at === -1 ? order.length : at;
	};
	return [...actions]
		.sort((a, b) => rank(a) - rank(b))
		.map((action) => action.charAt(0).toUpperCase() + action.slice(1))
		.join(" · ");
}
