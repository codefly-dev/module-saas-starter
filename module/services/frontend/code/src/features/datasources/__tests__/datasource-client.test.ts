import { afterEach, describe, expect, it, vi } from "vitest";
import { createDatasourceClient } from "../datasource-client";

// The composition declares which permission resource its collection content is
// governed by; the host has no noun of its own to fall back on. A generic stand-in
// is enough here — these tests assert the value is threaded, not what it is.
const CONTENT_RESOURCE = "example-records";

const datasourceClient = createDatasourceClient(CONTENT_RESOURCE);

afterEach(() => vi.unstubAllGlobals());

interface ScopeRequest {
	orgId?: string;
	resourceType?: string;
	action?: string;
	pageSize?: number;
	pageToken?: string;
}

const NODE_A = "11111111-1111-1111-1111-111111111111";
const NODE_B = "22222222-2222-2222-2222-222222222222";

function replyFor(request: ScopeRequest) {
	if (request.action === "read") {
		return request.pageToken
			? { scopes: [{ nodeId: NODE_B, label: "Specs", kind: "collection" }] }
			: {
					scopes: [{ nodeId: NODE_A, label: "Docs", kind: "collection" }],
					nextPageToken: "cursor-1",
				};
	}
	return { scopes: [{ nodeId: NODE_A, label: "Docs", kind: "collection" }] };
}

/**
 * The transport uses Connect's JSON codec, but its serializer hands `fetch` a
 * Uint8Array, so the body has to be decoded before it parses.
 */
function parseRequest(body: BodyInit | null | undefined): ScopeRequest {
	if (typeof body === "string") return JSON.parse(body) as ScopeRequest;
	const bytes =
		body instanceof Uint8Array
			? body
			: new Uint8Array(body as unknown as ArrayBuffer);
	return JSON.parse(new TextDecoder().decode(bytes)) as ScopeRequest;
}

/** Stubs fetch, holding every response until the returned `release` is called. */
function gatedFetch() {
	const requests: ScopeRequest[] = [];
	let release!: () => void;
	const gate = new Promise<void>((resolve) => {
		release = resolve;
	});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
			const request = parseRequest(init.body);
			requests.push(request);
			await gate;
			return new Response(JSON.stringify(replyFor(request)), {
				status: 200,
				headers: { "content-type": "application/json" },
			});
		}),
	);
	return { requests, release };
}

describe("datasourceClient.listAccessibleScopes", () => {
	it("queries the declared content resource and follows every page", async () => {
		const { requests, release } = gatedFetch();

		const pending = datasourceClient.listAccessibleScopes?.("org-1");

		await vi.waitFor(() => expect(requests).toHaveLength(1));
		release();
		const scopes = await pending;

		// Read access spans both pages.
		expect(requests).toHaveLength(2);
		expect(requests.filter((r) => r.action === "read")).toHaveLength(2);
		expect(requests.some((r) => r.pageToken === "cursor-1")).toBe(true);

		// The grant vocabulary and the page bound are what make the lookup return
		// anything at all; a silent change to either empties every boundary.
		for (const request of requests) {
			expect(request.orgId).toBe("org-1");
			expect(request.resourceType).toBe(CONTENT_RESOURCE);
			expect(request.pageSize).toBe(1000);
		}

		expect(scopes).toEqual([
			{
				nodeId: NODE_A,
				label: "Docs",
				kind: "collection",
				actions: ["read"],
			},
			{
				nodeId: NODE_B,
				label: "Specs",
				kind: "collection",
				actions: ["read"],
			},
		]);
	});
});

it("uses Accounts to create an exact read role and grant the selected team", async () => {
 const calls: {url: string; body: Record<string, unknown>}[] = [];
 vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => {
  const body = parseRequest(init.body) as Record<string, unknown>;
  calls.push({url: String(url), body});
  const response = String(url).endsWith("/CreateRole") ? {role: {id: "role-id"}} : {};
  return new Response(JSON.stringify(response), {headers: {"content-type": "application/json"}});
 }));
 await datasourceClient.grantCollectionRead!("org-1", "root.example", {id: "team-id", kind: "team", label: "Example Team"});
 expect(calls.map(call => call.url.split("/").at(-1))).toEqual(["ListRoles", "CreateRole", "GrantScope"]);
 expect(calls[1].body.permissions).toEqual([{resource: CONTENT_RESOURCE, action: "read"}]);
 // Named after its resource: a reader role for another resource (the dev-admin
 // fixture seeds "Collection reader" for its example resource) must not collide
 // with the organization-unique role name.
 expect(calls[1].body.name).toBe(`Collection reader (${CONTENT_RESOURCE})`);
 expect(calls[2].body).toMatchObject({orgId: "org-1", scopePath: "root.example", subjectId: "team-id", subjectKind: "SUBJECT_KIND_TEAM", roleId: "role-id"});
});

it("propagates permission-service failure rather than reporting no grants", async () => {
 vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({code: "unavailable", message: "permission service unavailable"}), {status: 503, headers: {"content-type": "application/json"}})));
 await expect(datasourceClient.listAccessibleScopes!("org-1")).rejects.toThrow("permission service unavailable");
 await expect(datasourceClient.listCollections!("org-1")).rejects.toThrow("permission service unavailable");
});

it("leaves read access unresolved when the composition declares no content resource", async () => {
 // An empty scope list would state that the viewer is refused; with no declared
 // resource there is no question to ask, so the lookup rejects instead.
 await expect(createDatasourceClient(undefined).listAccessibleScopes!("org-1")).rejects.toThrow(/no collection content resource is declared/);
});

it("refuses to grant when the composition declares no content resource", async () => {
 await expect(
  createDatasourceClient(undefined).grantCollectionRead!("org-1", "root.example", {id: "team-id", kind: "team", label: "Example Team"}),
 ).rejects.toThrow(/no collection content resource is declared/);
});

it("ignores a content resource inlined into the build", async () => {
 // The declaration arrives from the deployment's configuration at request
 // time; a build-time variable is empty in every deployed image and must not
 // decide what this client asks.
 process.env.NEXT_PUBLIC_COLLECTION_CONTENT_RESOURCE = "from-the-build";
 try {
  await expect(createDatasourceClient(undefined).listAccessibleScopes!("org-1")).rejects.toThrow(/no collection content resource is declared/);
 } finally {
  delete process.env.NEXT_PUBLIC_COLLECTION_CONTENT_RESOURCE;
 }
});
