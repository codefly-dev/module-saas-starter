"use client";

/**
 * RateLimitTracker — captures X-RateLimit-* headers from every
 * Connect response, exposes the latest snapshot to the UI via a
 * tiny pub-sub. The api sets these on every successful call (see
 * api/pkg/adapters/rate_limit_interceptor.go); the CORS config
 * exposes them so the browser can read them.
 *
 * Why a singleton instead of per-component state: the warning banner
 * needs a global view (any RPC call updates the budget), and most
 * consumers don't want to wire a query for it.
 */

import type { Interceptor } from "@connectrpc/connect";

export interface RateLimitSnapshot {
	// Total requests allowed in the current window.
	limit: number;
	// Remaining requests in the current window.
	remaining: number;
	// Unix epoch seconds when the window resets.
	resetAt: number;
	// Wall-clock when this snapshot was captured (ms since epoch).
	capturedAt: number;
}

let current: RateLimitSnapshot | null = null;
const subscribers = new Set<(s: RateLimitSnapshot) => void>();

function notify(s: RateLimitSnapshot) {
	for (const sub of subscribers) sub(s);
}

export function getRateLimit(): RateLimitSnapshot | null {
	return current;
}

export function subscribeRateLimit(
	fn: (s: RateLimitSnapshot) => void,
): () => void {
	subscribers.add(fn);
	return () => {
		subscribers.delete(fn);
	};
}

/**
 * rateLimitInterceptor — Connect interceptor that intercepts every
 * unary response, parses the rate-limit headers if present, and
 * notifies subscribers. Errors are not handled here; the rate-
 * limit error path returns headers in error.metadata which the
 * `update` hook still captures by reading from the catch branch.
 */
export const rateLimitInterceptor: Interceptor = (next) => async (req) => {
	try {
		const resp = await next(req);
		update(resp.header);
		return resp;
	} catch (err) {
		// ConnectError carries response headers in `metadata`; we read
		// those so a 429 still updates the snapshot.
		const meta = (err as { metadata?: Headers }).metadata;
		if (meta) update(meta);
		throw err;
	}
};

function update(h: Headers) {
	const limit = parseIntHeader(h.get("X-RateLimit-Limit"));
	const remaining = parseIntHeader(h.get("X-RateLimit-Remaining"));
	const resetAt = parseIntHeader(h.get("X-RateLimit-Reset"));
	if (limit === null || remaining === null || resetAt === null) return;
	const snap: RateLimitSnapshot = {
		limit,
		remaining,
		resetAt,
		capturedAt: Date.now(),
	};
	current = snap;
	notify(snap);
}

function parseIntHeader(v: string | null): number | null {
	if (!v) return null;
	const n = parseInt(v, 10);
	return Number.isFinite(n) ? n : null;
}
