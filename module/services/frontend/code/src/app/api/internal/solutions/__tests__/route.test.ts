import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// Both routes read the cluster-internal secret via the Codefly SDK. vi.mock is
// hoisted above module init, so the stub must be created with vi.hoisted.
const { getWorkspaceSecret } = vi.hoisted(() => ({
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
}));
vi.mock("codefly", () => ({ getWorkspaceSecret }));

import { GET } from "@/app/api/internal/solutions/route";
import {
	DELETE,
	POST,
	GET as publicGET,
} from "@/app/api/solutions/register/route";

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

async function register(): Promise<void> {
	getWorkspaceSecret.mockReturnValue(TOKEN);
	const response = await POST(
		new Request("http://frontend/api/solutions/register", {
			method: "POST",
			headers: {
				"content-type": "application/json",
				"x-codefly-internal-token": TOKEN,
			},
			body: JSON.stringify(MANIFEST),
		}),
	);
	expect(response.status).toBe(200);
}

describe("internal solution detail lookup", () => {
	afterEach(async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		await DELETE(
			new Request("http://frontend/api/solutions/register?id=audit", {
				method: "DELETE",
				headers: { "x-codefly-internal-token": TOKEN },
			}),
		);
		getWorkspaceSecret.mockReset();
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
		await register();

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

	it("serves detail the public navigation projection withholds", async () => {
		await register();

		const publicBody = (await publicGET().then((r) => r.json())) as {
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
