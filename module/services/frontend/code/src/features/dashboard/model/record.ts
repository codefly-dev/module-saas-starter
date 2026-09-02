import type { DashboardDef } from "./schema";

// How far a dashboard is visible inside its organization. `private` is the
// owner (and org admins) only; `org` shares it read-only with every member.
// Cross-user visibility is enforced server-side; the field is carried here so
// the record model is complete before the store that acts on it lands.
export type DashboardVisibility = "private" | "org";

export const DEFAULT_DASHBOARD_VISIBILITY: DashboardVisibility = "private";

// A named, owned dashboard: a spec plus the lifecycle metadata a spec literal
// never had. `orgId`/`ownerId` are assigned by the server that persists the
// record — the browser can't attest to tenancy or ownership — so they are not
// part of the client-owned shape. Where user and solution dashboards diverge is
// exactly here: the `spec` is shared, this lifecycle is new.
export interface DashboardRecord {
	readonly id: string;
	readonly name: string;
	readonly spec: DashboardDef;
	readonly visibility: DashboardVisibility;
	// ISO-8601 instants.
	readonly createdAt: string;
	readonly updatedAt: string;
}

// A partial update. An absent field is left unchanged; `name` and `spec` are
// validated when present. Renaming, saving a spec, and sharing are all one
// patch shape.
export interface DashboardRecordPatch {
	readonly name?: string;
	readonly spec?: DashboardDef;
	readonly visibility?: DashboardVisibility;
}

export class DashboardNameError extends Error {
	constructor(message = "A dashboard needs a name.") {
		super(message);
		this.name = "DashboardNameError";
	}
}

// A name is what a user picks a dashboard out of a list by, so an empty or
// whitespace-only one is rejected at the write boundary rather than stored.
export function assertDashboardName(name: string): void {
	if (name.trim() === "") throw new DashboardNameError();
}

export function isDashboardVisibility(
	value: unknown,
): value is DashboardVisibility {
	return value === "private" || value === "org";
}
