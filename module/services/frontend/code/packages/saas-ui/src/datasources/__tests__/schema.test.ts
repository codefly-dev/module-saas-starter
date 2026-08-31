import { describe, expect, it } from "vitest";
import { connectGitHubSchema } from "../schema.js";

const valid = {
	repo: "codefly-dev/module-saas-starter",
	paths: "docs/\nsrc/",
	branch: "main",
	targetCollection: "docs",
	accessToken: "ghp_token",
	webhookSecret: "",
};

describe("connectGitHubSchema", () => {
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
});
