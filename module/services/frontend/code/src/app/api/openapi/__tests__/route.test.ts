import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join, resolve } from "path";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GET } from "@/app/api/openapi/route";

// The route reads ../../api/openapi/user.swagger.json relative to the process
// cwd. Point cwd at a scratch tree so the real read is exercised without
// touching the repo — the fs builtin cannot be mocked in this runtime.
let root: string;

afterEach(() => {
	vi.restoreAllMocks();
	if (root) rmSync(root, { recursive: true, force: true });
});

function withCwdTree(specContents?: string): string {
	root = mkdtempSync(join(tmpdir(), "openapi-route-"));
	const cwd = join(root, "services", "frontend", "code");
	mkdirSync(cwd, { recursive: true });
	if (specContents !== undefined) {
		const specDir = resolve(cwd, "../../api/openapi");
		mkdirSync(specDir, { recursive: true });
		writeFileSync(join(specDir, "user.swagger.json"), specContents);
	}
	vi.spyOn(process, "cwd").mockReturnValue(cwd);
	return cwd;
}

describe("openapi route", () => {
	it("serves the spec file as JSON", async () => {
		const spec = JSON.stringify({ openapi: "3.0.0", paths: {} });
		withCwdTree(spec);

		const res = GET();

		expect(res.status).toBe(200);
		expect(res.headers.get("content-type")).toBe("application/json");
		await expect(res.text()).resolves.toBe(spec);
	});

	it("404s when the spec file is absent", async () => {
		withCwdTree();

		const res = GET();

		expect(res.status).toBe(404);
		await expect(res.json()).resolves.toEqual({
			error: "OpenAPI spec not found",
		});
	});
});
