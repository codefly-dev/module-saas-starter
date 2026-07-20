// Pure domain types for RBAC roles. No React, no framework imports.

export interface Permission {
	resource: string;
	action: string;
}

export interface Role {
	id: string;
	name: string;
	description: string;
	permissions: Permission[];
	builtIn: boolean;
	orgId: string;
}

export interface RoleAssignment {
	id: string;
	subjectId: string;
	subjectKind: number;
	roleId: string;
	orgId: string;
	scope: string;
}
