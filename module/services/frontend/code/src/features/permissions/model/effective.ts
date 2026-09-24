// Pure RBAC resolution. No React, no transport: given the grants an
// organization actually holds, answer the two questions an administrator asks
// — "what can this person do, and why" and "who can do this".
//
// This resolves the same rows the authorization service reads (role
// assignments joined to role permissions), but it is a client-side reading of
// them, not a decision by the policy decision point. Anything that must be
// authoritative asks the service.

export interface Permission {
	readonly resource: string;
	readonly action: string;
}

export interface Role {
	readonly id: string;
	readonly name: string;
	readonly permissions: readonly Permission[];
}

export interface Assignment {
	readonly subjectId: string;
	readonly roleId: string;
	// An assignment may be qualified by a scope, in which case the role it
	// grants applies only there. Dropping it here would render a narrow grant
	// as organization-wide authority, and would revoke nothing when acted on:
	// the delete predicate matches on scope too.
	readonly scope?: string;
}

export interface TeamRef {
	readonly id: string;
	readonly name: string;
}

// Where a grant came from. A permission an administrator cannot trace is a
// permission they cannot revoke: "via team" and "granted directly" need
// different actions, so they are different shapes rather than a label.
export type GrantSource =
	| {
			readonly via: "direct";
			readonly roleId: string;
			readonly roleName: string;
			readonly scope: string;
	  }
	| {
			readonly via: "team";
			readonly roleId: string;
			readonly roleName: string;
			readonly teamId: string;
			readonly teamName: string;
			readonly scope: string;
	  };

export interface EffectivePermission {
	readonly permission: string;
	readonly resource: string;
	readonly action: string;
	readonly sources: readonly GrantSource[];
}

export const permissionLabel = (permission: Permission): string =>
	`${permission.resource}:${permission.action}`;

// A stored grant may be a wildcard on either half ("*:*" is the platform
// role's shape), so holding a permission is not string equality.
export function grantCovers(grant: Permission, wanted: Permission): boolean {
	return (
		(grant.resource === "*" || grant.resource === wanted.resource) &&
		(grant.action === "*" || grant.action === wanted.action)
	);
}

export function parsePermission(label: string): Permission | undefined {
	const separator = label.indexOf(":");
	if (separator <= 0 || separator === label.length - 1) return undefined;
	return {
		resource: label.slice(0, separator),
		action: label.slice(separator + 1),
	};
}

interface EffectiveInput {
	readonly subjectId: string;
	readonly roles: readonly Role[];
	readonly assignments: readonly Assignment[];
	readonly teams: readonly TeamRef[];
}

// The subject's permissions with every path that grants them. Two roles
// granting the same permission collapse to one row with two sources, because
// that is the case where revoking one changes nothing.
export function resolveEffectivePermissions({
	subjectId,
	roles,
	assignments,
	teams,
}: EffectiveInput): EffectivePermission[] {
	const roleById = new Map(roles.map((role) => [role.id, role] as const));
	const teamById = new Map(teams.map((team) => [team.id, team] as const));

	const byPermission = new Map<
		string,
		{ permission: Permission; sources: GrantSource[] }
	>();
	const add = (permission: Permission, source: GrantSource) => {
		const label = permissionLabel(permission);
		const entry = byPermission.get(label) ?? { permission, sources: [] };
		entry.sources.push(source);
		byPermission.set(label, entry);
	};

	for (const assignment of assignments) {
		const role = roleById.get(assignment.roleId);
		if (!role) continue;
		const scope = assignment.scope ?? "";
		if (assignment.subjectId === subjectId) {
			for (const permission of role.permissions) {
				add(permission, {
					via: "direct",
					roleId: role.id,
					roleName: role.name,
					scope,
				});
			}
			continue;
		}
		const team = teamById.get(assignment.subjectId);
		if (!team) continue;
		for (const permission of role.permissions) {
			add(permission, {
				via: "team",
				roleId: role.id,
				roleName: role.name,
				teamId: team.id,
				teamName: team.name,
				scope,
			});
		}
	}

	return [...byPermission.entries()]
		.map(([label, entry]) => ({
			permission: label,
			resource: entry.permission.resource,
			action: entry.permission.action,
			sources: entry.sources,
		}))
		.sort((left, right) => left.permission.localeCompare(right.permission));
}

// The reverse question: which roles grant this permission, wildcards included.
export function rolesGranting(
	wanted: Permission,
	roles: readonly Role[],
): Role[] {
	return roles.filter((role) =>
		role.permissions.some((grant) => grantCovers(grant, wanted)),
	);
}

// Does this subject hold the permission at this scope, and by which paths?
// Answered from the effective set so a wildcard grant counts, which plain
// membership of the list would miss.
//
// scope is required rather than defaulted: a source carries the scope its
// assignment was granted at, so ignoring the question's scope returns paths
// that do not answer it — a role scoped to one project reported as an
// organization-wide grant. A caller that means "organization-wide" says so.
//
// The matching rule mirrors the store's (postgres_permissions.go, CheckPermission):
// an organization-wide assignment answers every question, and a scoped one
// answers only its own scope exactly. Diverging from it here would put a path
// list on screen beside a verdict that contradicts it.
export function sourcesGranting(
	wanted: Permission,
	effective: readonly EffectivePermission[],
	scope: string,
): GrantSource[] {
	return effective
		.filter((entry) => grantCovers(entry, wanted))
		.flatMap((entry) => entry.sources)
		.filter((source) => source.scope === "" || source.scope === scope);
}

export interface PermissionGroup {
	readonly resource: string;
	readonly permissions: readonly Permission[];
}

// The vocabulary grouped by the resource it acts on, which is how an
// administrator reads it — "what can be done to a webhook", not an alphabet.
export function groupByResource(
	vocabulary: readonly string[],
): PermissionGroup[] {
	const byResource = new Map<string, Permission[]>();
	for (const label of vocabulary) {
		const permission = parsePermission(label);
		if (!permission) continue;
		const group = byResource.get(permission.resource) ?? [];
		group.push(permission);
		byResource.set(permission.resource, group);
	}
	return [...byResource.entries()]
		.map(([resource, permissions]) => ({
			resource,
			permissions: permissions.sort((left, right) =>
				left.action.localeCompare(right.action),
			),
		}))
		.sort((left, right) => left.resource.localeCompare(right.resource));
}
