import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";
import {
	AuthProvider,
	exchangeRefreshCookie,
	resetRefreshExchangeForTests,
	useAuth,
} from "../auth";
import { setRefreshHandler, setToken } from "../connect/token-store";

function jwt(claims: Record<string, unknown>): string {
	return `h.${btoa(JSON.stringify(claims))}.s`;
}

function Probe() {
	const { isAuthenticated } = useAuth();
	return <span data-testid="authed">{isAuthenticated ? "yes" : "no"}</span>;
}

// Counts POST /v1/auth/refresh hits. Each hit represents one refresh-token
// rotation on the backend — the operation that must never run concurrently for
// the same cookie, or OWASP reuse detection revokes the whole session family.
function countingRefreshEndpoint(respond: () => Response): {
	count: () => number;
} {
	let hits = 0;
	server.use(
		http.post("/v1/auth/refresh", () => {
			hits += 1;
			return respond();
		}),
	);
	return { count: () => hits };
}

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	// The exchange single-flight is module-level state; clear it so a test that
	// left one pending can't hand a stale promise to the next case.
	resetRefreshExchangeForTests();
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("refresh-cookie exchange single-flight (#506)", () => {
	it("coalesces concurrent exchanges onto one rotation and shares the outcome", async () => {
		const endpoint = countingRefreshEndpoint(() =>
			HttpResponse.json({
				accessToken: jwt({ sub: "user-1" }),
				refreshToken: "r1",
			}),
		);

		// Two callers race for the same cookie (e.g. the StrictMode-doubled
		// bootstrap effect, or bootstrap vs. a mid-session interceptor). Without
		// coalescing the second would replay the token the first just rotated.
		const [a, b] = await Promise.all([
			exchangeRefreshCookie(),
			exchangeRefreshCookie(),
		]);

		expect(endpoint.count()).toBe(1);
		expect(a).toEqual({
			status: "ok",
			accessToken: jwt({ sub: "user-1" }),
			refreshToken: "r1",
		});
		expect(b).toEqual(a);
	});

	it("re-rotates on the next exchange once the in-flight one has settled", async () => {
		const endpoint = countingRefreshEndpoint(() =>
			HttpResponse.json({
				accessToken: jwt({ sub: "user-1" }),
				refreshToken: "r1",
			}),
		);

		await exchangeRefreshCookie();
		await exchangeRefreshCookie();

		// The single-flight only collapses *concurrent* callers; a later,
		// non-overlapping refresh (a new access-token expiry) must still rotate.
		expect(endpoint.count()).toBe(2);
	});

	it("issues exactly one bootstrap refresh even under React StrictMode", async () => {
		// StrictMode mounts, unmounts, and remounts effects in dev — which is how
		// `codefly run solution` (next dev) runs the app. The bootstrap exchange
		// must not fan that out into two concurrent rotations of the same cookie,
		// or the second self-trips reuse detection and the session is dead before
		// the first mid-session refresh.
		const endpoint = countingRefreshEndpoint(() =>
			HttpResponse.json({
				accessToken: jwt({ sub: "user-1" }),
				refreshToken: "r1",
			}),
		);

		render(
			<StrictMode>
				<AuthProvider>
					<Probe />
				</AuthProvider>
			</StrictMode>,
		);

		await waitFor(() =>
			expect(screen.getByTestId("authed").textContent).toBe("yes"),
		);
		expect(endpoint.count()).toBe(1);
	});

	it("routes the exchange through the cross-tab Web Lock when the platform supports it", async () => {
		// A second tab shares the origin's httpOnly refresh cookie but not this
		// module's in-memory single-flight, so two tabs whose access tokens lapse
		// together would each rotate the same cookie — the loser replays a consumed
		// token and self-trips reuse detection, revoking both. The exchange must run
		// under a shared platform lock. happy-dom has no real multi-runtime cookie
		// jar, so this pins the mechanism: the rotation happens inside
		// navigator.locks.request under the agreed key. Without the lock, request is
		// never called and cross-tab rotations race.
		const request = vi.fn(
			(_name: string, cb: (lock: unknown) => unknown): unknown =>
				Promise.resolve(cb(null)),
		);
		vi.stubGlobal("navigator", { ...globalThis.navigator, locks: { request } });

		const endpoint = countingRefreshEndpoint(() =>
			HttpResponse.json({
				accessToken: jwt({ sub: "user-1" }),
				refreshToken: "r1",
			}),
		);

		const outcome = await exchangeRefreshCookie();

		expect(request).toHaveBeenCalledWith(
			"codefly_auth_refresh",
			expect.any(Function),
		);
		expect(endpoint.count()).toBe(1);
		expect(outcome).toEqual({
			status: "ok",
			accessToken: jwt({ sub: "user-1" }),
			refreshToken: "r1",
		});
	});

	it("resolves to unavailable instead of rejecting when the exchange throws", async () => {
		// The bootstrap (`.then`) and mid-session handler (`await`) call sites carry
		// no error handling — they depend on the exchange never rejecting. Force a
		// throw at the fetch boundary (a stand-in for any future unguarded throw
		// inside the exchange) and assert the contract holds structurally: a
		// resolved `unavailable`, never a rejection that would surface as an
		// unhandled rejection or a thrown await that logs the user out on a hiccup.
		vi.spyOn(globalThis, "fetch").mockImplementation(() => {
			throw new Error("synchronous fetch failure");
		});

		await expect(exchangeRefreshCookie()).resolves.toEqual({
			status: "unavailable",
		});
	});
});
