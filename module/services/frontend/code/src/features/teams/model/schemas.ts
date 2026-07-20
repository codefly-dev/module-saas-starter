import { z } from "zod";

export const createTeamSchema = z.object({
	name: z.string().min(1, "Name is required").max(100, "Name too long"),
	description: z
		.string()
		.max(500, "Description too long")
		.optional()
		.default(""),
});

export type CreateTeamValues = z.infer<typeof createTeamSchema>;

export const addTeamMemberSchema = z.object({
	userId: z.string().min(1, "User ID is required"),
	role: z.enum(["member", "admin"]),
});

export type AddTeamMemberValues = z.infer<typeof addTeamMemberSchema>;
