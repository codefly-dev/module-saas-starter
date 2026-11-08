import { z } from "zod";

export const grantTeamSchema = z.object({
	teamId: z.string().min(1, "Choose a team"),
	roleId: z.string().min(1, "Choose the role the team is granted"),
});

export type GrantTeamValues = z.infer<typeof grantTeamSchema>;
