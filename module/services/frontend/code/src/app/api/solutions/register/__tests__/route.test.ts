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
import {
	findSolution,
	navProjection,
	type SolutionManifest,
} from "@/solutions/registry";

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

	it("lets the browser GET the nav list without a token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await GET();
		expect(res.status).toBe(200);
		await expect(res.json()).resolves.toHaveProperty("solutions");
	});

	it("projects the public nav list down to id and nav", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const body = manifestBody() as Record<string, unknown>;
		body.dashboard = {
			events: [{ name: "login", type: "auth.login.v1" }],
			metrics: [
				{
					id: "logins",
					kind: "source",
					filter: { event: "login" },
					groupBy: "time",
					bucket: "day",
					aggregation: "count",
				},
			],
			dashboards: [
				{
					id: "activity",
					layout: "grid",
					widgets: [{ id: "logins", metric: "logins", visualization: "line" }],
				},
			],
		};
		expect((await POST(postRequest(body, TOKEN))).status).toBe(200);

		const listed = (await GET().then((r) => r.json())) as {
			solutions: Array<Record<string, unknown>>;
		};
		const audit = listed.solutions.find((s) => s.id === "audit");
		expect(audit).toBeDefined();
		expect(audit?.nav).toMatchObject({ title: "Audit", path: "/s/audit" });
		// Everything the manifest carries beyond the nav entry is deployment
		// topology: where the solution's code is served from, which backend
		// fronts it, and its dashboard declaration. This response is readable by
		// every signed-in browser, so it must carry none of it.
		expect(Object.keys(audit ?? {}).sort()).toEqual(["id", "nav"]);
	});

	it("keeps a mutated nav projection out of the stored manifest", async () => {
		// The projection copies the nav object rather than aliasing it, so a
		// caller that mutates a projected value cannot reach the registry.
		//
		// This asserts against navProjection's own return value, NOT against a
		// parsed GET body: `await response.json()` is a fresh object, so mutating
		// it could never reach the registry however navProjection was written,
		// and a test framed that way passes with the aliasing bug in place.
		getWorkspaceSecret.mockReturnValue(TOKEN);
		expect((await POST(postRequest(manifestBody(), TOKEN))).status).toBe(200);

		const stored = findSolution("audit");
		expect(stored).not.toBeNull();
		const projected = navProjection(stored as SolutionManifest);
		projected.nav.title = "Tampered";

		expect(findSolution("audit")?.nav.title).toBe("Audit");
		const listed = (await GET().then((r) => r.json())) as {
			solutions: Array<{ id: string; nav: { title: string } }>;
		};
		expect(listed.solutions.find((s) => s.id === "audit")?.nav.title).toBe(
			"Audit",
		);
	});
});
