import type { Permission } from "./types";

export function formatPermission(p: Permission): string {
	return `${p.resource}:${p.action}`;
}

export function parsePermission(s: string): Permission | null {
	const [resource, action] = s.split(":");
	if (!resource || !action) return null;
	return { resource, action };
}
