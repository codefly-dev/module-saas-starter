/** Shape of module/fixtures/*.yaml — shared between server loader and client. */

import { z } from "zod";

export const FixtureUserSchema = z.object({
	email: z.string().email(),
	name: z.string().min(1),
	role: z.string().min(1),
	provider: z.string().min(1),
	provider_id: z.string().min(1),
	fixture_token: z.string().min(1).optional(),
});

export const FixtureOrgMemberSchema = z.object({
	email: z.string().email(),
	role: z.string().min(1),
});

export const FixtureOrgSchema = z.object({
	name: z.string().min(1),
	owner: z.string().email(),
	members: z.array(FixtureOrgMemberSchema).default([]),
});

export const FixtureTeamSchema = z.object({
	name: z.string().min(1),
	org: z.string().min(1),
	members: z.array(z.string()).default([]),
});

export const FixtureAgentSchema = z.object({
	org: z.string().min(1),
	agent_identifier: z.string().min(1),
	display_name: z.string().min(1).optional(),
	created_by: z.string().email(),
});

export const FixturePermissionSchema = z.object({
	resource: z.string().min(1),
	action: z.string().min(1),
});

export const FixtureRoleSchema = z.object({
	org: z.string().min(1),
	name: z.string().min(1),
	description: z.string().optional(),
	permissions: z.array(FixturePermissionSchema).min(1),
});

export const FixtureRoleAssignmentSchema = z.object({
	org: z.string().min(1),
	role: z.string().min(1),
	agent_identifier: z.string().min(1),
	scope: z.string().min(1).optional(),
});

export const FixtureFileSchema = z.object({
	users: z.array(FixtureUserSchema),
	organizations: z.array(FixtureOrgSchema).optional(),
	teams: z.array(FixtureTeamSchema).optional(),
	agents: z.array(FixtureAgentSchema).optional(),
	roles: z.array(FixtureRoleSchema).optional(),
	assignments: z.array(FixtureRoleAssignmentSchema).optional(),
});

export type FixtureUser = z.infer<typeof FixtureUserSchema>;
export type FixtureOrgMember = z.infer<typeof FixtureOrgMemberSchema>;
export type FixtureOrg = z.infer<typeof FixtureOrgSchema>;
export type FixtureTeam = z.infer<typeof FixtureTeamSchema>;
export type FixtureAgent = z.infer<typeof FixtureAgentSchema>;
export type FixturePermission = z.infer<typeof FixturePermissionSchema>;
export type FixtureRole = z.infer<typeof FixtureRoleSchema>;
export type FixtureRoleAssignment = z.infer<typeof FixtureRoleAssignmentSchema>;
export type FixtureFile = z.infer<typeof FixtureFileSchema>;
