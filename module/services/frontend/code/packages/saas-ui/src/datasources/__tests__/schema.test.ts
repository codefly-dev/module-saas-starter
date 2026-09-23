import { describe, expect, it } from "vitest";
import { connectGitHubSchema } from "../schema.js";

const valid = {
	method: "pat" as const,
	repo: "codefly-dev/module-saas-starter",
	paths: "docs/\nsrc/",
	branch: "main",
	targetCollection: "docs",
	accessToken: "ghp_token",
	webhookSecret: "",
};

describe("connectGitHubSchema", () => {
	it("accepts extension filters and rejects globs or paths", () => {
		for (const fileExtensions of [undefined, "", ".md", ".MD, .mdx"]) {
			expect(
				connectGitHubSchema.safeParse({ ...valid, fileExtensions }).success,
			).toBe(true);
		}
		for (const fileExtensions of ["md", "**/*.md", "../md", ".md/file"]) {
			expect(
				connectGitHubSchema.safeParse({ ...valid, fileExtensions }).success,
			).toBe(false);
		}
	});
	it("accepts a well-formed connect payload", () => {
		expect(connectGitHubSchema.safeParse(valid).success).toBe(true);
	});

	it("rejects a repo longer than the proto's 255-char bound", () => {
		const long = `${"a".repeat(256)}/repo`;
		expect(
			connectGitHubSchema.safeParse({ ...valid, repo: long }).success,
		).toBe(false);
	});

	it("rejects more than 64 path prefixes", () => {
		const paths = Array.from({ length: 65 }, (_v, i) => `p${i}`).join("\n");
		expect(connectGitHubSchema.safeParse({ ...valid, paths }).success).toBe(
			false,
		);
	});

	it("rejects a path prefix longer than 512 chars", () => {
		const paths = "x".repeat(513);
		expect(connectGitHubSchema.safeParse({ ...valid, paths }).success).toBe(
			false,
		);
	});

	it("accepts a payload written before the method discriminator existed", () => {
		// connectGitHubSchema is exported, so a consumer's payload predating the
		// App path must keep parsing — under the token rule it was written against.
		const legacy: Record<string, unknown> = { ...valid };
		delete legacy.method;

		const parsed = connectGitHubSchema.safeParse(legacy);

		expect(parsed.success).toBe(true);
		// Absent, not defaulted: a default lands in the inferred output type as a
		// required field, which is a breaking change for those same callers.
		expect(parsed.success && parsed.data.method).toBeUndefined();
		expect(
			connectGitHubSchema.safeParse({ ...legacy, accessToken: undefined })
				.success,
		).toBe(false);
	});

	it("waives the token only for the App and public methods", () => {
		expect(
			connectGitHubSchema.safeParse({
				...valid,
				method: "app",
				accessToken: undefined,
			}).success,
		).toBe(true);
		// The host still refuses a repository GitHub does not report public; the
		// form only stops demanding a token it would not send.
		expect(
			connectGitHubSchema.safeParse({
				...valid,
				method: "public",
				accessToken: undefined,
			}).success,
		).toBe(true);
		expect(
			connectGitHubSchema.safeParse({
				...valid,
				method: "pat",
				accessToken: undefined,
			}).success,
		).toBe(false);
	});
});
