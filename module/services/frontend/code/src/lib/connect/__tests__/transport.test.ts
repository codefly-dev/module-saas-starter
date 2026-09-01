import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	authedFetch,
	refreshToken,
	setRefreshHandler,
	setToken,
} from "../token-store";
import { authInterceptor } from "../transport";

afterEach(() => {
	setToken(null);
	setRefreshHandler(null);
	vi.unstubAllGlobals();
});

describe("token-store refresh coordination", () => {
	it("returns null when no handler is registered", async () => {
		await expect(refreshToken()).resolves.toBeNull();
	});

	it("shares one in-flight refresh across concurrent callers", async () => {
		let resolveRefresh: (token: string) => void = () => {};
		const handler = vi.fn(
			() =>
				new Promise<string | null>((resolve) => {
					resolveRefresh = resolve;
				}),
		);
		setRefreshHandler(handler);

		const first = refreshToken();
		const second = refreshToken();
		resolveRefresh("fresh-token");

		await expect(first).resolves.toBe("fresh-token");
		await expect(second).resolves.toBe("fresh-token");
		expect(handler).toHaveBeenCalledTimes(1);

		// A later call after settling starts a new refresh.
		setRefreshHandler(async () => "second-token");
		await expect(refreshToken()).resolves.toBe("second-token");
	});

	it("maps a rejecting handler to null", async () => {
		setRefreshHandler(() => Promise.reject(new Error("network down")));
		await expect(refreshToken()).resolves.toBeNull();
	});
});

type FakeRequest = { header: Headers };

function fakeRequest(): FakeRequest {
	return { header: new Headers() };
}

// The interceptor only touches req.header and forwards the request, so a
// minimal header-only shape exercises it without the full Connect machinery.
const intercept = authInterceptor as unknown as (
	next: (req: FakeRequest) => Promise<unknown>,
) => (req: FakeRequest) => Promise<unknown>;

const unauthenticated = new ConnectError("expired", Code.Unauthenticated);

describe("authInterceptor 401 recovery", () => {
	it("refreshes once and retries with the new token", async () => {
		setToken("stale-token");
		setRefreshHandler(async () => "fresh-token");

		const next = vi
			.fn<(req: FakeRequest) => Promise<unknown>>()
			.mockRejectedValueOnce(unauthenticated)
			.mockResolvedValueOnce("rpc-result");
		const req = fakeRequest();

		await expect(intercept(next)(req)).resolves.toBe("rpc-result");
		expect(next).toHaveBeenCalledTimes(2);
		expect(req.header.get("Authorization")).toBe("Bearer fresh-token");
	});

	it("rethrows when the refresh fails (session gone)", async () => {
		setToken("stale-token");
		setRefreshHandler(async () => null);

		const next = vi.fn().mockRejectedValue(unauthenticated);
		await expect(intercept(next)(fakeRequest())).rejects.toBe(unauthenticated);
		expect(next).toHaveBeenCalledTimes(1);
	});

	it("does not refresh on non-Unauthenticated errors", async () => {
		setToken("stale-token");
		const handler = vi.fn(async () => "fresh-token");
		setRefreshHandler(handler);

		const failure = new ConnectError("boom", Code.Internal);
		const next = vi.fn().mockRejectedValue(failure);
		await expect(intercept(next)(fakeRequest())).rejects.toBe(failure);
		expect(handler).not.toHaveBeenCalled();
		expect(next).toHaveBeenCalledTimes(1);
	});

	it("does not refresh when the call carried no token", async () => {
		const handler = vi.fn(async () => "fresh-token");
		setRefreshHandler(handler);

		const next = vi.fn().mockRejectedValue(unauthenticated);
		await expect(intercept(next)(fakeRequest())).rejects.toBe(unauthenticated);
		expect(handler).not.toHaveBeenCalled();
	});
});

// authedFetch always dispatches a Request object, so read the bearer off it.
function bearerOf(input: RequestInfo | URL): string | null {
	return input instanceof Request ? input.headers.get("Authorization") : null;
}

describe("authedFetch 401 recovery", () => {
	it("stamps the bearer token and returns a healthy response without refreshing", async () => {
		setToken("access-token");
		const handler = vi.fn(async () => "fresh-token");
		setRefreshHandler(handler);
		const fetchMock = vi
			.fn<typeof fetch>()
			.mockResolvedValue(new Response("ok", { status: 200 }));
		vi.stubGlobal("fetch", fetchMock);

		const res = await authedFetch("/api/thing");

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(bearerOf(fetchMock.mock.calls[0][0])).toBe("Bearer access-token");
		expect(handler).not.toHaveBeenCalled();
	});

	it("refreshes once and retries with the fresh token on a 401", async () => {
		setToken("stale-token");
		setRefreshHandler(async () => "fresh-token");
		const fetchMock = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(new Response(null, { status: 401 }))
			.mockResolvedValueOnce(new Response("ok", { status: 200 }));
		vi.stubGlobal("fetch", fetchMock);

		const res = await authedFetch("/api/thing", { method: "POST" });

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(2);
		expect(bearerOf(fetchMock.mock.calls[0][0])).toBe("Bearer stale-token");
		expect(bearerOf(fetchMock.mock.calls[1][0])).toBe("Bearer fresh-token");
		// The retry preserves the caller's request init.
		expect((fetchMock.mock.calls[1][0] as Request).method).toBe("POST");
	});

	it("replays a one-shot request body on the retry after refresh", async () => {
		setToken("stale-token");
		setRefreshHandler(async () => "fresh-token");
		const bodies: string[] = [];
		const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (req) => {
			bodies.push(await (req as Request).text());
			return new Response(null, { status: bodies.length === 1 ? 401 : 200 });
		});
		vi.stubGlobal("fetch", fetchMock);

		// A Request's body is a one-shot stream — reusing the sent request on the
		// retry (the pre-fix behavior) threw "body already used"; cloning before
		// the first dispatch lets the refreshed retry replay it.
		const res = await authedFetch(
			new Request("/api/upload", { method: "POST", body: "payload" }),
		);

		expect(res.status).toBe(200);
		expect(bodies).toEqual(["payload", "payload"]);
		expect(bearerOf(fetchMock.mock.calls[1][0])).toBe("Bearer fresh-token");
	});

	it("returns the 401 when the refresh fails (session gone)", async () => {
		setToken("stale-token");
		setRefreshHandler(async () => null);
		const fetchMock = vi
			.fn<typeof fetch>()
			.mockResolvedValue(new Response(null, { status: 401 }));
		vi.stubGlobal("fetch", fetchMock);

		const res = await authedFetch("/api/thing");

		expect(res.status).toBe(401);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("does not refresh when no token was installed", async () => {
		const handler = vi.fn(async () => "fresh-token");
		setRefreshHandler(handler);
		const fetchMock = vi
			.fn<typeof fetch>()
			.mockResolvedValue(new Response(null, { status: 401 }));
		vi.stubGlobal("fetch", fetchMock);

		const res = await authedFetch("/api/thing");

		expect(res.status).toBe(401);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(bearerOf(fetchMock.mock.calls[0][0])).toBeNull();
		expect(handler).not.toHaveBeenCalled();
	});
});
