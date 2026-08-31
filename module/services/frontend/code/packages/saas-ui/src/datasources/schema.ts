import { z } from "zod";

// Field bounds mirror the AddGitHubSource protobuf validation so the form
// rejects the same inputs the backend would.
export const connectGitHubSchema = z.object({
	repo: z
		.string()
		.regex(
			/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/,
			"Use the owner/name form, e.g. codefly-dev/module-saas-starter",
		),
	paths: z.string().optional(),
	branch: z.string().max(255, "Branch name too long").optional(),
	targetCollection: z
		.string()
		.min(1, "Target collection is required")
		.max(255, "Target collection too long"),
	accessToken: z
		.string()
		.min(1, "Access token is required")
		.max(1024, "Access token too long"),
	webhookSecret: z.string().max(1024, "Webhook secret too long").optional(),
});

export type ConnectGitHubValues = z.infer<typeof connectGitHubSchema>;
