/** UserProfileValues is the typed view of the open profile metadata map. */
export interface UserProfileValues {
	name?: string;
	display_name?: string;
	avatar_url?: string;
	title?: string;
	bio?: string;
	pronouns?: string;
	phone?: string;
	location?: string;
	timezone?: string;
}

/** stringProfilePatch removes absent typed values before merging into protobuf metadata. */
export function stringProfilePatch(
	patch: Partial<UserProfileValues>,
): Record<string, string> {
	return Object.fromEntries(
		Object.entries(patch).filter(
			(entry): entry is [string, string] => typeof entry[1] === "string",
		),
	);
}

/**
 * applyProfilePatch merges a typed patch onto existing metadata and drops
 * cleared (empty) values, so a blanked field is removed from the map rather
 * than persisted as an empty string. Unrelated keys are preserved.
 */
export function applyProfilePatch(
	base: Record<string, string> | undefined,
	patch: Partial<UserProfileValues>,
): Record<string, string> {
	const merged = { ...base, ...stringProfilePatch(patch) };
	return Object.fromEntries(
		Object.entries(merged).filter(([, value]) => value !== ""),
	);
}

/** profileInitials renders a compact, deterministic avatar fallback. */
export function profileInitials(value?: string) {
	const clean = value?.trim();
	if (!clean) return "U";
	const parts = clean.split(/\s+/).filter(Boolean);
	if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
	return `${parts[0][0]}${parts[1][0]}`.toUpperCase();
}
