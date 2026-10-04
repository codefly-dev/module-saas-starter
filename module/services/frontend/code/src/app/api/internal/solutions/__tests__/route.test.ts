
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// Both routes read the cluster-internal secret via the Codefly SDK, and the
// registry reaches the gateway through service discovery. vi.mock is hoisted
// above module init, so the stubs must be created with vi.hoisted.
const { getWorkspaceSecret, getEndpoints } = vi.hoisted(() => ({
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
}));
vi.mock("codefly", () => ({ getWorkspaceSecret, getEndpoints }));

const GATEWAY = "http://gateway.internal:8080";

// Registrations live in the durable registry behind the gateway rather than in
// this process, so the suite stands one in for it.
function fakeGateway() {
	const stored = new Map([[MANIFEST.id, JSON.stringify(MANIFEST)]]);
	let revision = 0;
	const respond = (body: unknown, status = 200) =>
		new Response(JSON.stringify(body), {
			status,
			headers: { "content-type": "application/json" },
		});
	return vi.fn(async (input: string | URL, init?: RequestInit) => {
		const url = new URL(String(input));
		if (url.pathname === "/solutions/_registry") {
			return respond({
				revision,
				solutions: [...stored].map(([id, manifest]) => ({
					// The host stamps the target from the record's declaration;
					// the fixture derives it from the alias so the entitlement
					// answer below can agree with it, which is what the join
					// needs. See projections.ts.
					targetId: `target-${id}`,
					id,
					status: "active",
					manifest,
				})),
			});
		}
		// The viewer projection beside this one is authenticated and narrowed per
		// viewer (#949); this file compares the two projections' FIELDS, so every
		// registered solution is entitled here.
		if (url.pathname === "/solutions/_entitlements") {
			return respond({
				org: "org-acme",
				viewer: "viewer-1",
				solutions: [...stored.keys()].map((id) => ({
					targetId: `target-${id}`,
					healthy: true,
					scopeNodeId: `node-${id}`,
				})),
			});
		}
		return respond({ error: "unexpected" }, 500);
	});
}

function resetRegistryCache() {
	const g = globalThis as Record<string, unknown>;
	g.__solutionSnapshot = null;
	g.__solutionSnapshotInFlight = null;
}

import { GET } from "@/app/api/internal/solutions/route";
import { GET as publicGET } from "@/app/api/solutions/route";

const TOKEN = "internal-test-token";

const MANIFEST = {
	id: "audit",
	nav: { title: "Audit", path: "/s/audit" },
	frontend: {
		type: "module-federation",
		manifestUrl: "https://audit.internal/mf-manifest.json",
		exposedModule: "./Page",
	},
	backend: { serviceAlias: "audit-backend" },
	dashboard: {
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
	},
};

function internalRequest(token?: string): Request {
	const headers: Record<string, string> = {};
	if (token !== undefined) {
		headers["x-codefly-internal-token"] = token;
	}
	return new Request("http://frontend/api/internal/solutions", { headers });
}


describe("internal solution detail lookup", () => {
	beforeEach(() => {
		resetRegistryCache();
		getEndpoints.mockReturnValue([
			{ service: "auth-gateway", name: "rest", address: `${GATEWAY}/rest` },
		]);
		vi.stubGlobal("fetch", fakeGateway());
	});

	afterEach(() => {
		getWorkspaceSecret.mockReset();
		vi.unstubAllGlobals();
		resetRegistryCache();
	});

	it("rejects a caller with no internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		expect((await GET(internalRequest())).status).toBe(401);
	});

	it("rejects a caller with the wrong internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		expect((await GET(internalRequest("not-the-token"))).status).toBe(401);
	});

	it("fails closed when no internal secret is configured", async () => {
		getWorkspaceSecret.mockReturnValue(undefined);
		expect((await GET(internalRequest(TOKEN))).status).toBe(401);
	});

	it("serves the remote and backend detail to a trusted caller", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);

		const response = await GET(internalRequest(TOKEN));
		expect(response.status).toBe(200);
		const body = (await response.json()) as {
			solutions: Array<Record<string, unknown>>;
		};
		const audit = body.solutions.find((s) => s.id === "audit");
		expect(audit).toBeDefined();
		expect(audit?.frontend).toMatchObject({
			manifestUrl: MANIFEST.frontend.manifestUrl,
			exposedModule: "./Page",
		});
		expect(audit?.backend).toMatchObject({ serviceAlias: "audit-backend" });
		// The dashboard graph is read in-process by the solution page, never over
		// HTTP, so no reader would spend the bytes.
		expect(audit).not.toHaveProperty("dashboard");
	});

	it("serves detail the viewer navigation projection withholds", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);

		const viewerRequest = new Request("http://frontend/api/solutions", {
			headers: { authorization: "Bearer viewer-token" },
		});
		const publicBody = (await publicGET(viewerRequest).then((r) =>
			r.json(),
		)) as {
			solutions: Array<Record<string, unknown>>;
		};
		const publicAudit = publicBody.solutions.find((s) => s.id === "audit");
		expect(publicAudit).not.toHaveProperty("frontend");
		expect(publicAudit).not.toHaveProperty("backend");

		const internalBody = (await GET(internalRequest(TOKEN)).then((r) =>
			r.json(),
		)) as { solutions: Array<Record<string, unknown>> };
		expect(internalBody.solutions.find((s) => s.id === "audit")).toHaveProperty(
			"frontend",
		);
	});
});
