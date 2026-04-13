/**
 * SSE endpoint that proxies notification unread counts from the backend.
 *
 * The browser's EventSource connects here; we poll the Connect RPC
 * endpoint server-side every 5 seconds and push updates. Using plain
 * fetch avoids a dependency on @connectrpc/connect-node which is not
 * installed.
 */

const GATEWAY_URL =
  process.env.NEXT_PUBLIC_API_CONNECT || "http://localhost:8080";

async function getUnreadCount(authHeader: string | null): Promise<number> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (authHeader) {
    headers["Authorization"] = authHeader;
  }
  // Connect protocol: unary POST with JSON body to the RPC method URL.
  const res = await fetch(
    `${GATEWAY_URL}/customers.NotificationService/GetUnreadCount`,
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
  // EventSource doesn't support custom headers, so we also accept the
  // token as a query parameter. The header takes priority.
  const url = new URL(req.url);
  const tokenParam = url.searchParams.get("token");
  const authHeader =
    req.headers.get("Authorization") ||
    (tokenParam ? `Bearer ${tokenParam}` : null);
  const encoder = new TextEncoder();

  const stream = new ReadableStream({
    start(controller) {
      const push = (data: unknown) => {
        controller.enqueue(
          encoder.encode(`data: ${JSON.stringify(data)}\n\n`),
        );
      };

      const poll = async () => {
        try {
          const count = await getUnreadCount(authHeader);
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
