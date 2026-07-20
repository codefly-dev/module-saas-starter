/**
 * SSE endpoint that proxies notification unread counts from the backend.
 *
 * The browser connects with a streaming fetch carrying Authorization; we poll the Connect RPC
 * endpoint server-side every 5 seconds and push updates. Using plain
 * fetch avoids a dependency on @connectrpc/connect-node which is not
 * installed.
 */

import { requireAccountsConnect } from "../../../../../server/accounts-bindings.mjs";

async function getUnreadCount(
	connectURL: string,
	authHeader: string | null,
): Promise<number> {
	const headers: Record<string, string> = {
		"Content-Type": "application/json",
	};
	if (authHeader) {
		headers["Authorization"] = authHeader;
	}
	// Connect protocol: unary POST with JSON body to the RPC method URL.
	const res = await fetch(
		`${connectURL}/saas.accounts.v1.NotificationService/GetUnreadCount`,
		{
			method: "POST",
			headers,
			body: "{}",
		},
	);
	if (!res.ok) return 0;
	const data = (await res.json()) as { count?: number };
	return data.count ?? 0;
}

export async function GET(req: Request) {
	let connectURL: string;
	try {
		connectURL = requireAccountsConnect();
	} catch {
		return Response.json(
			{ error: "notification backend unavailable" },
			{ status: 503 },
		);
	}
	// Never accept credentials in URLs; query strings leak through logs, traces,
	// history, and referrer metadata.
	const authHeader = req.headers.get("Authorization");
	const encoder = new TextEncoder();

	const stream = new ReadableStream({
		start(controller) {
			const push = (data: unknown) => {
				controller.enqueue(encoder.encode(`data: ${JSON.stringify(data)}\n\n`));
			};

			const poll = async () => {
				try {
					const count = await getUnreadCount(connectURL, authHeader);
					push({ unreadCount: count });
				} catch {
					// Swallow errors — client will reconnect on stream close.
				}
			};

			// Fire immediately, then every 5 seconds.
			void poll();
			const interval = setInterval(poll, 5_000);

			req.signal.addEventListener("abort", () => {
				clearInterval(interval);
				controller.close();
			});
		},
	});

	return new Response(stream, {
		headers: {
			"Content-Type": "text/event-stream",
			"Cache-Control": "no-cache",
			Connection: "keep-alive",
		},
	});
}
