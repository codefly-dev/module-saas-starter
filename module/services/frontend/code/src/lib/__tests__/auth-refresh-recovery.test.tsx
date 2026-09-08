import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, useAuth } from "../auth";
import { refreshToken, setRefreshHandler, setToken } from "../connect/token-store";

function jwt(claims: Record<string, unknown>): string {
	return `h.${btoa(JSON.stringify(claims))}.s`;
}

function Probe() {
	const { isAuthenticated, isLoading } = useAuth();
	return (
		<span data-testid="authed">
			{isLoading ? "loading" : isAuthenticated ? "yes" : "no"}
		</span>
	);
}

// Queued responses for POST /v1/auth/refresh: the first is consumed by the
// AuthProvider's initial-load exchange, the rest by explicit refreshToken()
// calls — exactly what the Connect transport and the solutions' authedFetch
// trigger on a mid-session 401.
let refreshQueue: Array<() => Response>;

function installRefreshEndpoint() {
	refreshQueue = [];
	server.use(
		http.post("/v1/auth/refresh", () => {
			const next = refreshQueue.shift();
			return next ? next() : new HttpResponse(null, { status: 500 });
		}),
	);
}

const okBody = (access: string, refresh: string) => () =>
	HttpResponse.json({ accessToken: access, refreshToken: refresh });
const withStatus = (code: number) => () => new HttpResponse(null, { status: code });

async function renderAuthenticated() {
	installRefreshEndpoint();
	refreshQueue.push(okBody(jwt({ sub: "user-1" }), "refresh-1"));
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
	await waitFor(() =>
		expect(screen.getByTestId("authed").textContent).toBe("yes"),
	);
}

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	vi.restoreAllMocks();
});

describe("on-load bootstrap refresh", () => {
	it("issues exactly ONE refresh exchange on a transient 5xx and lands unauthenticated without retrying", async () => {
		// /v1/auth/refresh is reached through a Next same-origin rewrite, so a
		// cold-start failure is a 5xx *response* that still carried the single-use
		// refresh cookie the browser sent. Re-presenting it (the old on-load retry
		// loop) is refresh-token reuse and, under strict OWASP rotation, revokes
		// the entire session family across every device. The bootstrap must present
		// the cookie AT MOST ONCE.
		let calls = 0;
		server.use(
			http.post("/v1/auth/refresh", () => {
				calls += 1;
				return new HttpResponse(null, { status: 503 });
			}),
		);

		render(
			<AuthProvider>
				<Probe />
			</AuthProvider>,
		);

		// Boot resolves to unauthenticated (loading cleared). The old loop would
		// still be mid-backoff here; the single-shot bootstrap is already settled.
		await waitFor(() =>
			expect(screen.getByTestId("authed").textContent).toBe("no"),
		);
		expect(calls).toBe(1);

		// And it stays 1 — no delayed retry fires after settling. The old loop
		// issued up to 5 POSTs at 300ms/600ms/... spacing; wait past that window.
		await new Promise((resolve) => setTimeout(resolve, 400));
		expect(calls).toBe(1);
	});

	it("does not clear a still-valid refresh cookie on a transient bootstrap failure", async () => {
		// "unavailable" must NOT tear down the httpOnly session: a later reload once
		// the backend is warm should refresh cleanly. clearRefreshToken() would
		// discard the local mirror of a cookie that is still valid server-side.
		const clearSpy = vi.spyOn(Storage.prototype, "removeItem");
		server.use(
			http.post(
				"/v1/auth/refresh",
				() => new HttpResponse(null, { status: 503 }),
			),
		);

		render(
			<AuthProvider>
				<Probe />
			</AuthProvider>,
		);
		await waitFor(() =>
			expect(screen.getByTestId("authed").textContent).toBe("no"),
		);

		expect(clearSpy).not.toHaveBeenCalledWith("codefly_refresh_token");
	});

	it("tears down and lands unauthenticated on an authoritative expired (401)", async () => {
		let calls = 0;
		server.use(
			http.post("/v1/auth/refresh", () => {
				calls += 1;
				return new HttpResponse(null, { status: 401 });
			}),
		);

		render(
			<AuthProvider>
				<Probe />
			</AuthProvider>,
		);
		await waitFor(() =>
			expect(screen.getByTestId("authed").textContent).toBe("no"),
		);
		// An authoritative rejection is also a single exchange — never retried.
		expect(calls).toBe(1);
	});
});

describe("mid-session refresh handler recovery", () => {
	it("redirects to login and tears down state when the session is expired (401)", async () => {
		const replace = vi
			.spyOn(window.location, "replace")
			.mockImplementation(() => {});
		await renderAuthenticated();

		refreshQueue.push(withStatus(401));
		let result: string | null = "unset";
		await act(async () => {
			result = await refreshToken();
		});

		expect(result).toBeNull();
		expect(replace).toHaveBeenCalledTimes(1);
		expect(replace.mock.calls[0][0]).toMatch(/^\/auth\/login\?next=/);
		expect(screen.getByTestId("authed").textContent).toBe("no");
	});

	it("keeps the session and does NOT redirect on a transient failure (503)", async () => {
		const replace = vi
			.spyOn(window.location, "replace")
			.mockImplementation(() => {});
		await renderAuthenticated();

		refreshQueue.push(withStatus(503));
		let result: string | null = "unset";
		await act(async () => {
			result = await refreshToken();
		});

		// A gateway hiccup fails only this request; the still-valid session must
		// survive — no forced logout, no redirect.
		expect(result).toBeNull();
		expect(replace).not.toHaveBeenCalled();
		expect(screen.getByTestId("authed").textContent).toBe("yes");
	});

	it("installs the fresh token and does NOT redirect on a successful refresh", async () => {
		const replace = vi
			.spyOn(window.location, "replace")
			.mockImplementation(() => {});
		await renderAuthenticated();

		refreshQueue.push(okBody(jwt({ sub: "user-1" }), "refresh-2"));
		let result: string | null = null;
		await act(async () => {
			result = await refreshToken();
		});

		expect(result).toBe(jwt({ sub: "user-1" }));
		expect(replace).not.toHaveBeenCalled();
		expect(screen.getByTestId("authed").textContent).toBe("yes");
	});
});
