import { z } from "zod";

export const createTeamSchema = z.object({
	name: z
		.string()
		.trim()
		.min(1, "Name is required")
		.max(100, "Use 100 characters or fewer"),
	description: z
		.string()
		.max(500, "Use 500 characters or fewer")
		.optional()
		.default(""),
});

export type CreateTeamValues = z.infer<typeof createTeamSchema>;

export const addTeamMemberSchema = z.object({
	userId: z.string().min(1, "User ID is required"),
	role: z.enum(["member", "admin"]),
});

export type AddTeamMemberValues = z.infer<typeof addTeamMemberSchema>;
