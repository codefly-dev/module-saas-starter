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

function toLocaleDate(iso: string | undefined): string | undefined {
	if (!iso) return undefined;
	const date = new Date(iso);
	return Number.isNaN(date.getTime()) ? undefined : date.toLocaleDateString();
}

export function formatSyncedAt(iso: string | undefined): string {
	return toLocaleDate(iso) ?? "Never";
}

/**
 * The ingest-provenance line: when the change-set compiler last enqueued a
 * delivery, and the head commit that delivery covered. Undefined before the
 * first delivery lands, so the row renders no line at all rather than a second
 * "Never" that would read as the sync clock having two values.
 */
export function formatIngest(
	iso: string | undefined,
	commit: string | undefined,
): string | undefined {
	const when = toLocaleDate(iso);
	if (!when) return undefined;
	return commit
		? `last webhook ingest ${when} · ${commit.slice(0, 7)}`
		: `last webhook ingest ${when}`;
}
