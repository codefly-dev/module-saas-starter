// @vitest-environment node
import { createServer, type Server, type RequestListener } from "node:http";
import { once } from "node:events";
import { beforeAll, afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));
const { getEndpoints, findSolution } = vi.hoisted(() => ({
	getEndpoints: vi.fn(),
	findSolution: vi.fn(),
}));
vi.mock("codefly", () => ({
	getEndpoints,
	getCurrentModule: () => "",
	getCurrentService: () => "",
	getWorkspaceSecret: () => "fixture-internal",
}));
vi.mock("@/solutions/registry", () => ({ findSolution }));
import { server as mockServer } from "@/test/setup";
// Exercise native fetch/socket cancellation without MSW response cloning.
beforeAll(() => mockServer.close());

import { GET, POST } from "../route";

const context = {
	params: Promise.resolve({
		id: "example",
		path: ["runs", "saved-run", "events"],
	}),
};
const url = "http://frontend/api/solutions/example/proxy/runs/saved-run/events";
const encoder = new TextEncoder();
let server: Server | undefined;
afterEach(async () => {
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
	if (server) {
		server.closeAllConnections();
		await new Promise<void>((resolve) => server!.close(() => resolve()));
		server = undefined;
	}
	findSolution.mockReset();
	getEndpoints.mockReset();
});
function registered() {
	findSolution.mockResolvedValue({
		backend: { serviceAlias: "example-backend" },
	});
	getEndpoints.mockReturnValue([
		{
			service: "auth-gateway",
			name: "rest",
			address: "http://gateway.internal",
		},
	]);
}
async function upstream(handler: RequestListener) {
	registered();
	server = createServer(handler);
	server.listen(0, "127.0.0.1");
	await once(server, "listening");
	const address = server.address();
	if (!address || typeof address === "string")
		throw new Error("fixture listener missing");
	getEndpoints.mockReturnValue([
		{
			service: "auth-gateway",
			name: "rest",
			address: `http://127.0.0.1:${address.port}`,
		},
	]);
}
function request(headers: Record<string, string> = {}, signal?: AbortSignal) {
	return new Request(url, {
		headers: {
			authorization: "Bearer fixture-user",
			accept: "text/event-stream",
			...headers,
		},
		signal,
	});
}

describe("authenticated solution stream carrier", () => {
	it("delivers bytes before EOF and forwards the bounded cursor on the next GET", async () => {
		const calls: Array<{
			method?: string;
			path?: string;
			resume?: string;
			auth?: string;
		}> = [];
		let end: (() => void) | undefined;
		await upstream((req, res) => {
			calls.push({
				method: req.method,
				path: req.url,
				resume: req.headers["last-event-id"] as string | undefined,
				auth: req.headers.authorization,
			});
			res.writeHead(200, {
				"content-type": "text/event-stream; charset=utf-8",
				"cache-control": "public",
				"x-accel-buffering": "yes",
				"set-cookie": "private=fixture",
				"x-internal-secret": "fixture",
			});
			res.write(encoder.encode(": heartbeat\n\n"));
			end = () =>
				res.end(
					encoder.encode(
						'id: saved-run:2\nevent: snapshot\ndata: {"text":"café"}\n\n',
					),
				);
		});
		for (const cursor of [undefined, "saved-run:2"]) {
			const response = await GET(
				request(cursor ? { "last-event-id": cursor } : {}),
				context,
			);
			expect(response.headers.get("cache-control")).toBe(
				"no-store, no-transform",
			);
			expect(response.headers.get("x-accel-buffering")).toBe("no");
			expect(response.headers.has("set-cookie")).toBe(false);
			expect(response.headers.has("x-internal-secret")).toBe(false);
			const reader = response.body!.getReader();
			expect((await reader.read()).value).toEqual(
				encoder.encode(": heartbeat\n\n"),
			);
			end!();
			let rest = "";
			const decoder = new TextDecoder();
			for (;;) {
				const next = await reader.read();
				if (next.done) break;
				rest += decoder.decode(next.value, { stream: true });
			}
			expect(rest + decoder.decode()).toBe(
				'id: saved-run:2\nevent: snapshot\ndata: {"text":"café"}\n\n',
			);
		}
		expect(calls).toEqual(
			[undefined, "saved-run:2"].map((resume) => ({
				method: "GET",
				path: "/solutions/example-backend/runs/saved-run/events",
				resume,
				auth: "Bearer fixture-user",
			})),
		);
	});

	it.each(["abort", "cancel"])(
		"closes upstream observation on browser %s without another request",
		async (mode) => {
			let closed = false,
				calls = 0;
			await upstream((req, res) => {
				calls++;
				req.on("close", () => {
					closed = true;
				});
				res.writeHead(200, { "content-type": "text/event-stream" });
				res.write(": ready\n\n");
			});
			const abort = new AbortController();
			const response = await GET(request({}, abort.signal), context);
			const reader = response.body!.getReader();
			await reader.read();
			if (mode === "abort") {
				abort.abort();
				await expect(reader.read()).rejects.toMatchObject({
					name: "AbortError",
				});
			} else await reader.cancel();
			await vi.waitFor(() => expect(closed).toBe(true));
			expect(calls).toBe(1);
		},
	);

	it("does not follow redirects carrying caller or internal credentials", async () => {
		const paths: string[] = [];
		await upstream((req, res) => {
			paths.push(req.url!);
			res.writeHead(307, { location: "/credential-sink" });
			res.end();
		});
		vi.spyOn(console, "error").mockImplementation(() => {});
		expect((await GET(request(), context)).status).toBe(502);
		expect(paths).toEqual(["/solutions/example-backend/runs/saved-run/events"]);
	});

	it("keeps an authorization denial intact without reconnecting", async () => {
		let calls = 0;
		await upstream((_req, res) => {
			calls++;
			res.writeHead(401, {
				"content-type": "application/json",
				"x-request-id": "denied-1",
			});
			res.end('{"error":"denied"}');
		});
		vi.spyOn(console, "warn").mockImplementation(() => {});
		const response = await GET(
			request({ "last-event-id": "saved-run:2" }),
			context,
		);
		expect(response.status).toBe(401);
		expect(await response.json()).toEqual({ error: "denied" });
		expect(response.headers.get("x-codefly-solution-error")).toBe("auth");
		expect(calls).toBe(1);
	});

	it("rejects oversized cursors before any registry or gateway request", async () => {
		const response = await GET(
			request({ "last-event-id": "a".repeat(1025) }),
			context,
		);
		expect(response.status).toBe(400);
		expect(findSolution).not.toHaveBeenCalled();
	});

	it("does not forward resume hints on a mutation or retry that mutation", async () => {
		registered();
		const fetch = vi.fn().mockResolvedValue(new Response("ok"));
		vi.stubGlobal("fetch", fetch);
		await POST(
			new Request(url, {
				method: "POST",
				body: "{}",
				headers: { "last-event-id": "saved-run:2" },
			}),
			context,
		);
		expect(fetch).toHaveBeenCalledTimes(1);
		const init = fetch.mock.calls[0][1];
		expect(init.headers.has("last-event-id")).toBe(false);
		expect(init.cache).toBe("no-store");
	});
});
