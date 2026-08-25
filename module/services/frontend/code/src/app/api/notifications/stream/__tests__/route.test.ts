import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { requireAccountsConnect } = vi.hoisted(() => ({
	requireAccountsConnect: vi.fn<() => string>(),
}));
vi.mock("../../../../../../server/accounts-bindings.mjs", () => ({
	requireAccountsConnect,
}));

import { GET } from "@/app/api/notifications/stream/route";

const CONNECT = "http://accounts.internal:9000";

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
	fetchMock = vi.fn(async () => new Response(JSON.stringify({ count: 3 })));
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	requireAccountsConnect.mockReset();
	vi.unstubAllGlobals();
});

// Read only the first (immediate) SSE frame. The caller aborts the request
// afterwards so the route clears its poll interval and closes the stream —
// cancelling the reader here would double-close the controller.
async function firstFrame(res: Response): Promise<string> {
	const reader = res.body?.getReader();
	if (!reader) throw new Error("response had no body stream");
	const { value } = await reader.read();
	reader.releaseLock();
	return new TextDecoder().decode(value);
}

describe("notifications SSE stream", () => {
	it("503s when the notification backend is unresolvable", async () => {
		requireAccountsConnect.mockImplementation(() => {
			throw new Error("gateway required");
		});

		const res = await GET(
			new Request("http://frontend/api/notifications/stream"),
		);

		expect(res.status).toBe(503);
		await expect(res.json()).resolves.toEqual({
			error: "notification backend unavailable",
		});
	});

	it("responds with an event-stream carrying the first unread count", async () => {
		requireAccountsConnect.mockReturnValue(CONNECT);
		const controller = new AbortController();

		const res = await GET(
			new Request("http://frontend/api/notifications/stream", {
				headers: { Authorization: "Bearer caller-token" },
				signal: controller.signal,
			}),
		);

		expect(res.headers.get("content-type")).toBe("text/event-stream");
		expect(res.headers.get("cache-control")).toBe("no-cache");

		const frame = await firstFrame(res);
		expect(frame).toBe(`data: ${JSON.stringify({ unreadCount: 3 })}\n\n`);
		controller.abort();
	});

	it("polls the Connect RPC with the caller Authorization, never from the URL", async () => {
		requireAccountsConnect.mockReturnValue(CONNECT);
		const controller = new AbortController();

		const res = await GET(
			new Request(
				"http://frontend/api/notifications/stream?access_token=leaked",
				{
					headers: { Authorization: "Bearer caller-token" },
					signal: controller.signal,
				},
			),
		);
		await firstFrame(res);
		controller.abort();

		expect(fetchMock).toHaveBeenCalledWith(
			`${CONNECT}/saas.accounts.v1.NotificationService/GetUnreadCount`,
			expect.objectContaining({
				method: "POST",
				headers: expect.objectContaining({
					Authorization: "Bearer caller-token",
				}),
				body: "{}",
			}),
		);
	});

	it("reports zero unread when the backend rejects the poll", async () => {
		requireAccountsConnect.mockReturnValue(CONNECT);
		fetchMock.mockResolvedValueOnce(new Response("nope", { status: 500 }));
		const controller = new AbortController();

		const res = await GET(
			new Request("http://frontend/api/notifications/stream", {
				headers: { Authorization: "Bearer caller-token" },
				signal: controller.signal,
			}),
		);

		const frame = await firstFrame(res);
		expect(frame).toBe(`data: ${JSON.stringify({ unreadCount: 0 })}\n\n`);
		controller.abort();
	});
});
