/** Presentation only: group membership never participates in an access decision. */
export interface SolutionGroup {
	id: string;
	title: string;
	order: number;
}

export function parseSolutionGroup(
	value: unknown,
): SolutionGroup | undefined | null {
	if (value === undefined) return undefined;
	if (typeof value !== "object" || value === null || Array.isArray(value))
		return null;
	const group = value as Record<string, unknown>;
	if (
		Object.keys(group).some((key) => !["id", "title", "order"].includes(key)) ||
		typeof group.id !== "string" ||
		!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(group.id) ||
		group.id.length > 1024 ||
		typeof group.title !== "string" ||
		group.title.trim() === "" ||
		new TextEncoder().encode(group.title).length > 1024 ||
		/[\u0000-\u001f\u007f-\u009f]/.test(group.title) ||
		!Number.isSafeInteger(group.order) ||
		(group.order as number) < -2147483648 ||
		(group.order as number) > 2147483647
	)
		return null;
	return { id: group.id, title: group.title, order: group.order as number };
}

/** Group an already-authorized projection; this function performs no access check. */
export function groupSolutionNavigation<T extends { group?: SolutionGroup }>(
	items: T[],
): Array<{ key: string; title: string; items: T[] }> {
	const groups = new Map<string, { group: SolutionGroup; items: T[] }>();
	const ungrouped: T[] = [];
	for (const item of items) {
		if (!item.group) {
			ungrouped.push(item);
			continue;
		}
		const existing = groups.get(item.group.id);
		if (existing) existing.items.push(item);
		else groups.set(item.group.id, { group: item.group, items: [item] });
	}
	const sections = [...groups.values()]
		.sort(
			(a, b) =>
				a.group.order - b.group.order || a.group.id.localeCompare(b.group.id),
		)
		.map(({ group, items }) => ({
			key: `group:${group.id}`,
			title: group.title,
			items,
		}));
	if (ungrouped.length)
		sections.push({
			key: "ungrouped",
			title: sections.length ? "Other" : "Solutions",
			items: ungrouped,
		});
	return sections;
}
