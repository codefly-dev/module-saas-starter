import { z } from "zod";

// What an installer fills in. The bounds mirror InstallSolutionRequest's own
// validation so a value the host would refuse on shape is caught in the form;
// everything the host decides from its own state — whether the target is still
// installable, whether an audience is one it serves, whether the role exists —
// is left to the host, whose refusal is shown in its own words.
export const installSolutionSchema = z.object({
	agentIdentifier: z
		.string()
		.trim()
		.min(1, "An agent identifier is required")
		.max(512, "At most 512 characters"),
	displayName: z.string().trim().max(512, "At most 512 characters"),
	rootScopeLabel: z.string().trim().max(512, "At most 512 characters"),
	roleId: z.string().min(1, "Choose the role the solution's agent is granted"),
	allowedAudiences: z.string(),
	allowedScopes: z.string(),
});

export type InstallSolutionValues = z.infer<typeof installSolutionSchema>;
