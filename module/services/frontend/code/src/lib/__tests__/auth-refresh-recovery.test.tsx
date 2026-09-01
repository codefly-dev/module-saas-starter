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
	const { isAuthenticated } = useAuth();
	return <span data-testid="authed">{isAuthenticated ? "yes" : "no"}</span>;
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
