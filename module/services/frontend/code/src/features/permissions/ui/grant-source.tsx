"use client";

import { Badge } from "@/shared/ui";
import type { GrantSource } from "../model/effective";

// A grant's provenance is permissions-domain presentation, so it lives beside
// the resolution that produces it rather than inside whichever page rendered
// it first.

// Two paths to one permission are distinct rows in the UI, and a scope is part
// of what makes them distinct: the same role granted org-wide and within one
// scope are different grants, revoked separately.
export function sourceKey(source: GrantSource): string {
	const scope = source.scope ? `@${source.scope}` : "";
	return source.via === "team"
		? `team:${source.teamId}:${source.roleId}${scope}`
		: `direct:${source.roleId}${scope}`;
}

export function GrantSourceBadge({ source }: { source: GrantSource }) {
	const via =
		source.via === "team"
			? `${source.roleName} via ${source.teamName}`
			: source.roleName;
	return (
		<Badge variant={source.via === "team" ? "outline" : "secondary"}>
			{source.scope ? `${via} in ${source.scope}` : via}
		</Badge>
	);
}
