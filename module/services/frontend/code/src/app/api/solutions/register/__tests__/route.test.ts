import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The route reads the cluster-internal secret via the Codefly SDK. vi.mock is
// hoisted above module init, so the stub must be created with vi.hoisted.
const { getWorkspaceSecret } = vi.hoisted(() => ({
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
}));
vi.mock("codefly", () => ({ getWorkspaceSecret }));

import { DELETE, GET, POST } from "@/app/api/solutions/register/route";

const TOKEN = "internal-test-token";

function manifestBody(id = "audit") {
	return {
		id,
		nav: { title: "Audit", path: `/s/${id}` },
		frontend: {
			type: "module-federation",
			manifestUrl: "https://audit.internal/mf-manifest.json",
			exposedModule: "./Page",
		},
	};
}

function postRequest(body: unknown, token?: string): Request {
	const headers: Record<string, string> = {
		"content-type": "application/json",
	};
	if (token !== undefined) {
		headers["x-codefly-internal-token"] = token;
	}
	return new Request("http://frontend/api/solutions/register", {
		method: "POST",
		headers,
		body: JSON.stringify(body),
	});
}

describe("solutions register route auth", () => {
	afterEach(async () => {
		getWorkspaceSecret.mockReset();
		// Clean any registration this suite added.
		getWorkspaceSecret.mockReturnValue(TOKEN);
		await DELETE(
			new Request("http://frontend/api/solutions/register?id=audit", {
				method: "DELETE",
				headers: { "x-codefly-internal-token": TOKEN },
			}),
		);
		getWorkspaceSecret.mockReset();
	});

	it("rejects a POST with no internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(postRequest(manifestBody()));
		expect(res.status).toBe(401);
	});

	it("rejects a POST with the wrong internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(postRequest(manifestBody(), "not-the-token"));
		expect(res.status).toBe(401);
	});

	it("fails closed when no internal secret is configured", async () => {
		getWorkspaceSecret.mockReturnValue(undefined);
		const res = await POST(postRequest(manifestBody(), TOKEN));
		expect(res.status).toBe(401);
	});

	it("accepts a POST with the correct internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(postRequest(manifestBody(), TOKEN));
		expect(res.status).toBe(200);
		await expect(res.json()).resolves.toMatchObject({ ok: true, id: "audit" });
	});

	it("rejects an authenticated POST carrying an unsafe manifest", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const bad = manifestBody("evil");
		(bad.frontend as Record<string, unknown>).manifestUrl =
			"javascript:alert(1)";
		const res = await POST(postRequest(bad, TOKEN));
		expect(res.status).toBe(422);
	});

	it("lets the browser GET the nav list without a token, nav-only", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		// Register a solution so the list is non-empty.
		await POST(postRequest(manifestBody(), TOKEN));

		const res = await GET();
		expect(res.status).toBe(200);
		const body = (await res.json()) as {
			solutions: Array<Record<string, unknown>>;
		};
		expect(body.solutions.length).toBeGreaterThan(0);
		for (const solution of body.solutions) {
			expect(Object.keys(solution).sort()).toEqual(["id", "nav"]);
			// The MF manifest URL and backend alias must not be broadcast here.
			expect(solution).not.toHaveProperty("frontend");
			expect(solution).not.toHaveProperty("backend");
		}
	});
});
