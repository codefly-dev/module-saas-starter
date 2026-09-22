import { generateKeyPairSync, sign } from "node:crypto";

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
	const stored = new Map<string, string>();
	let revision = 0;
	const respond = (body: unknown, status = 200) =>
		new Response(JSON.stringify(body), {
			status,
			headers: { "content-type": "application/json" },
		});
	return vi.fn(async (input: string | URL, init?: RequestInit) => {
		const url = new URL(String(input));
		if (url.pathname === "/v1/auth/.well-known/jwks.json") {
			return new Response(CRED_JWKS, { status: 200 });
		}
		if (url.pathname === "/solutions/_frontend") {
			const body = JSON.parse(String(init?.body ?? "{}"));
			stored.set(body.id, body.manifest);
			revision += 1;
			return respond({ ok: true, id: body.id, revision, status: "active" });
		}
		if (url.pathname === "/solutions/_register" && init?.method === "DELETE") {
			stored.delete(url.searchParams.get("id") ?? "");
			revision += 1;
			return respond({ ok: true, revision });
		}
		if (url.pathname === "/solutions/_registry") {
			return respond({
				revision,
				leaseSeconds: 120,
				solutions: [...stored].map(([id, manifest]) => ({
					id,
					status: "active",
					manifest,
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


const { publicKey: credPublicKey, privateKey: credPrivateKey } =
	generateKeyPairSync("ed25519");
const CRED_KEY_ID = "test-key";
const CRED_JWKS = JSON.stringify({
	keys: [
		{
			...credPublicKey.export({ format: "jwk" }),
			alg: "EdDSA",
			use: "sig",
			kid: CRED_KEY_ID,
		},
	],
});
let credCounter = 0;

/** Registration is credential-bound now; this file only needs a valid one. */
function solutionCredential(solution = "audit"): string {
	const b64 = (v: object) =>
		Buffer.from(JSON.stringify(v), "utf8").toString("base64url");
	const now = Math.floor(Date.now() / 1000);
	const head = b64({ alg: "EdDSA", typ: "JWT", kid: CRED_KEY_ID });
	const payload = b64({
		iss: "saas-starter",
		sub: `solution:${solution}`,
		aud: ["solution-registration"],
		solution,
		iat: now,
		exp: now + 300,
		jti: `internal-jti-${credCounter++}`,
	});
	return `${head}.${payload}.${sign(null, Buffer.from(`${head}.${payload}`, "utf8"), credPrivateKey).toString("base64url")}`;
}

async function register(): Promise<void> {
	getWorkspaceSecret.mockReturnValue(TOKEN);
	const response = await POST(
		new Request("http://frontend/api/solutions/register", {
			method: "POST",
			headers: {
				"content-type": "application/json",
				"x-codefly-internal-token": TOKEN,
				"x-codefly-solution-registration": solutionCredential(),
			},
			body: JSON.stringify(MANIFEST),
		}),
	);
	expect(response.status).toBe(200);
}

describe("internal solution detail lookup", () => {
	beforeEach(() => {
		resetRegistryCache();
		getEndpoints.mockReturnValue([
			{ service: "auth-gateway", name: "rest", address: `${GATEWAY}/rest` },
		]);
		const g = globalThis as Record<string, unknown>;
		g.__solutionRegistrationJwks = undefined;
		g.__solutionRegistrationJtis = undefined;
		vi.stubGlobal("fetch", fakeGateway());
	});

	afterEach(async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		await DELETE(
			new Request("http://frontend/api/solutions/register?id=audit", {
				method: "DELETE",
				headers: {
					"x-codefly-internal-token": TOKEN,
					"x-codefly-solution-registration": solutionCredential(),
				},
			}),
		);
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
