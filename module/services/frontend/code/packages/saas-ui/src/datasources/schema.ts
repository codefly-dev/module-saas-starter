import { z } from "zod";
import { parsePaths } from "./util.js";

// Field bounds mirror the AddGitHubSource protobuf validation so the form
// rejects the same inputs the backend would.
export const connectGitHubSchema = z
	.object({
		// How the source authenticates. The App path sends no token at all —
		// the host resolves the installation covering the repository — so the
		// token bound below is conditional rather than a flat `min(1)`.
		//
		// Defaulted because this schema is exported: a payload written against an
		// earlier version carries no `method`, and `pat` is the rule it was
		// written under.
		method: z.enum(["app", "pat"]).default("pat"),
		repo: z
			.string()
			.max(255, "Repository name too long")
			.regex(
				/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/,
				"Use the owner/name form, e.g. codefly-dev/module-saas-starter",
			),
		paths: z
			.string()
			.optional()
			.refine((raw) => parsePaths(raw).length <= 64, "Too many paths (max 64)")
			.refine(
				(raw) => parsePaths(raw).every((path) => path.length <= 512),
				"A path prefix is too long (max 512 characters)",
			),
		branch: z.string().max(255, "Branch name too long").optional(),
		boundaryNodeId: z.string().optional(),
		targetCollection: z
			.string()
			.min(1, "Target collection is required")
			.max(255, "Target collection too long"),
		accessToken: z.string().max(1024, "Access token too long").optional(),
		webhookSecret: z.string().max(1024, "Webhook secret too long").optional(),
	})
	.superRefine((values, ctx) => {
		if (values.method === "pat" && !values.accessToken) {
			ctx.addIssue({
				code: z.ZodIssueCode.custom,
				path: ["accessToken"],
				message: "Access token is required",
			});
		}
	});

export type ConnectGitHubValues = z.infer<typeof connectGitHubSchema>;
