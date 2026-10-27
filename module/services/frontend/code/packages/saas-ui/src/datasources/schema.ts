import { z } from "zod";
import { parseLines, parsePaths } from "./util.js";

/**
 * The three fields that select what a source fetches, as both the connect form
 * and the edit form bind them. They are shared rather than restated because
 * connecting and editing are the same decision made twice: a bound that held
 * on only one of the two would let an edit store a scope that connecting
 * refuses, or refuse one it accepts.
 */
const fetchScopeFields = {
	paths: z
		.string()
		.optional()
		.refine((raw) => parsePaths(raw).length <= 64, "Too many paths (max 64)")
		.refine(
			(raw) => parsePaths(raw).every((path) => path.length <= 512),
			"A path prefix is too long (max 512 characters)",
		),
	branch: z.string().max(255, "Branch name too long").optional(),
	fileExtensions: z
		.string()
		.optional()
		.refine(
			(raw) => parsePaths(raw).length <= 32,
			"Too many extensions (max 32)",
		)
		.refine(
			(raw) =>
				parsePaths(raw).every((value) =>
					/^\.[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(value),
				),
			"Use suffixes such as .md or .mdx, separated by commas; no paths or globs",
		),
} as const;

/**
 * What an edit of a connected source may change. Only the fetch scope: the
 * repository, the credential and the collection are the source's identity and
 * are not editable, so they are deliberately absent rather than disabled.
 */
export const editSourceSchema = z.object(fetchScopeFields);

export type EditSourceValues = z.infer<typeof editSourceSchema>;

// Field bounds mirror the AddGitHubSource protobuf validation so the form
// rejects the same inputs the backend would.
export const connectGitHubSchema = z
	.object({
		// How the source authenticates. The App path sends no token at all —
		// the host resolves the installation covering the repository — and
		// neither does the public path, which the host accepts only once GitHub
		// confirms the repository is public. So the token bound below is
		// conditional rather than a flat `min(1)`.
		//
		// Optional rather than defaulted because this schema is exported: a
		// default lands in the inferred output type as a *required* field, which
		// would force every external caller constructing these values to name a
		// mode that did not exist before. Absent therefore reads as the pre-App
		// rule everywhere — only `app` and `public` relax the token.
		method: z.enum(["app", "public", "pat"]).optional(),
		repo: z
			.string()
			.max(255, "Repository name too long")
			.regex(
				/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/,
				"Use the owner/name form, e.g. codefly-dev/module-saas-starter",
			),
		paths: fetchScopeFields.paths,
		branch: fetchScopeFields.branch,
		fileExtensions: fetchScopeFields.fileExtensions,
		boundaryNodeId: z.string().optional(),
		targetCollection: z
			.string()
			.min(1, "Target collection is required")
			.max(255, "Target collection too long"),
		accessToken: z.string().max(1024, "Access token too long").optional(),
		webhookSecret: z.string().max(1024, "Webhook secret too long").optional(),
	})
	.check((ctx) => {
		const values = ctx.value;
		if (
			values.method !== "app" &&
			values.method !== "public" &&
			!values.accessToken
		) {
			ctx.issues.push({
				input: values,
				code: "custom",
				path: ["accessToken"],
				message: "Access token is required",
			});
		}
	});

export type ConnectGitHubValues = z.infer<typeof connectGitHubSchema>;

// The host's bounds on a key-pattern list. Whether each is valid RE2 is the
// host's to say: a browser's RegExp is a different dialect.
const keyPatterns = z
	.string()
	.optional()
	.refine((raw) => parseLines(raw).length <= 16, "Too many patterns (max 16)")
	.refine(
		(raw) => parseLines(raw).every((line) => line.length <= 256),
		"A pattern is too long (max 256 characters)",
	);

export const connectObjectStorageSchema = z.object({
	endpoint: z
		.string()
		.max(2048, "Endpoint too long")
		.regex(/^https:\/\/[^\s/]+/, "Use the store's https URL"),
	region: z.string().min(1, "Region is required").max(64, "Region too long"),
	bucket: z.string().min(1, "Bucket is required").max(255, "Bucket too long"),
	prefix: z.string().max(1024, "Prefix too long"),
	includePatterns: keyPatterns,
	excludePatterns: keyPatterns,
	fileExtensions: fetchScopeFields.fileExtensions,
	accessKeyId: z
		.string()
		.min(1, "Access key ID is required")
		.max(256, "Access key ID too long"),
	secretAccessKey: z
		.string()
		.min(1, "Secret access key is required")
		.max(4096, "Secret access key too long"),
	boundaryNodeId: z.string().optional(),
	targetCollection: z
		.string()
		.min(1, "Target collection is required")
		.max(255, "Target collection too long"),
});

export type ConnectObjectStorageValues = z.infer<
	typeof connectObjectStorageSchema
>;
