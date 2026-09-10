import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, useAuth } from "../auth";
import {
	getToken,
	refreshToken,
	setRefreshHandler,
	setToken,
} from "../connect/token-store";

const ADMIN = "019f6c01-0001-7000-8000-000000000001";
const TARGET = "019f6c01-0002-7000-8000-000000000002";

function jwt(claims: Record<string, unknown>): string {
	return `h.${btoa(JSON.stringify(claims))}.s`;
}

const adminToken = jwt({ sub: ADMIN, email: "support@example.com" });
const impersonationToken = jwt({ sub: ADMIN, acting: TARGET });

// The probe publishes the two context handles through a mutable holder rather
// than assigning module-scope bindings during render, which is a render-time
// side effect the React compiler rejects.
const handles: {
	enter?: (accessToken: string) => void;
	exit?: () => Promise<void>;
} = {};

function enter(accessToken: string): void {
	if (!handles.enter) throw new Error("probe is not mounted");
	handles.enter(accessToken);
}

function exit(): Promise<void> {
	if (!handles.exit) throw new Error("probe is not mounted");
	return handles.exit();
}

function Probe() {
	const {
		impersonation,
		isAuthenticated,
		enterImpersonation,
		exitImpersonation,
	} = useAuth();
	useEffect(() => {
		handles.enter = enterImpersonation;
		handles.exit = exitImpersonation;
	}, [enterImpersonation, exitImpersonation]);
	return (
		<span data-testid="state">
			{isAuthenticated ? "authed" : "anon"}/
			{impersonation.isImpersonating ? impersonation.subjectId : "self"}
		</span>
	);
}

let refreshQueue: Array<() => Response>;
let logoutCalls: number;

async function renderAsAdmin() {
	refreshQueue = [];
	logoutCalls = 0;
	server.use(
		http.post("/v1/auth/refresh", () => {
			const next = refreshQueue.shift();
			return next ? next() : new HttpResponse(null, { status: 500 });
		}),
		http.post("/v1/auth/logout", () => {
			logoutCalls += 1;
			return HttpResponse.json({});
		}),
	);
	refreshQueue.push(() =>
		HttpResponse.json({ accessToken: adminToken, refreshToken: "refresh-1" }),
	);
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
	await waitFor(() =>
		expect(screen.getByTestId("state").textContent).toBe("authed/self"),
	);
}

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	vi.restoreAllMocks();
});

describe("Impersonation session journey", () => {
	// Entering installs the impersonation token as the active session and names
	// the target, not the admin, as the user being viewed.
	it("enters an impersonated session", async () => {
		await renderAsAdmin();

		act(() => enter(impersonationToken));

		await waitFor(() =>
			expect(screen.getByTestId("state").textContent).toBe(`authed/${TARGET}`),
		);
		expect(getToken()).toBe(impersonationToken);
	});

	// Exiting restores the admin's own session from their refresh cookie, which
	// impersonation never touched, and retains no target context.
	it("exits back to the original session with no target context", async () => {
		await renderAsAdmin();
		act(() => enter(impersonationToken));
		await waitFor(() =>
			expect(screen.getByTestId("state").textContent).toBe(`authed/${TARGET}`),
		);

		refreshQueue.push(() =>
			HttpResponse.json({ accessToken: adminToken, refreshToken: "refresh-2" }),
		);
		await act(async () => {
			await exit();
		});

		expect(screen.getByTestId("state").textContent).toBe("authed/self");
		expect(getToken()).toBe(adminToken);
		expect(logoutCalls).toBe(0);
	});

	// If the admin's own session can no longer be re-established, the exit must
	// not leave the impersonation token installed.
	it("logs out when the original session cannot be restored", async () => {
		await renderAsAdmin();
		act(() => enter(impersonationToken));
		await waitFor(() =>
			expect(screen.getByTestId("state").textContent).toBe(`authed/${TARGET}`),
		);

		refreshQueue.push(() => new HttpResponse(null, { status: 401 }));
		await act(async () => {
			await exit();
		});

		expect(screen.getByTestId("state").textContent).toBe("anon/self");
		expect(getToken()).toBeNull();
		expect(logoutCalls).toBe(1);
	});

	// An impersonation token has no refresh half, so the 401 that follows its
	// expiry exchanges the ADMIN's cookie. Restoring that session is correct —
	// replaying the request that triggered it is not: it was issued as the
	// target and would execute with the admin's authority, auditing as an
	// ordinary action.
	it("does not replay an impersonated request under the admin's authority", async () => {
		await renderAsAdmin();
		act(() => enter(impersonationToken));
		await waitFor(() =>
			expect(screen.getByTestId("state").textContent).toBe(`authed/${TARGET}`),
		);

		refreshQueue.push(() =>
			HttpResponse.json({ accessToken: adminToken, refreshToken: "refresh-2" }),
		);
		// refreshToken() is the exact entry point the transport interceptor and
		// authedFetch use on a 401; both retry only when it yields a token.
		let replayToken: string | null = null;
		await act(async () => {
			replayToken = await refreshToken();
		});

		// The caller gets no token, so the in-flight request fails instead of
		// re-running as the admin...
		expect(replayToken).toBeNull();
		// ...while the admin's own session is restored rather than logged out.
		await waitFor(() =>
			expect(screen.getByTestId("state").textContent).toBe("authed/self"),
		);
		expect(logoutCalls).toBe(0);
	});

	// An ordinary session still retries normally — the guard must be scoped to
	// impersonated sessions, not a blanket change to 401 recovery.
	it("still replays an ordinary request after a refresh", async () => {
		await renderAsAdmin();

		refreshQueue.push(() =>
			HttpResponse.json({ accessToken: adminToken, refreshToken: "refresh-2" }),
		);
		let replayToken: string | null = null;
		await act(async () => {
			replayToken = await refreshToken();
		});

		expect(replayToken).toBe(adminToken);
	});

	// A transient restore failure must not destroy the admin's session: the
	// refresh cookie is untouched and the operator can retry.
	it("keeps the impersonated session when the restore is transiently unavailable", async () => {
		await renderAsAdmin();
		act(() => enter(impersonationToken));
		await waitFor(() =>
			expect(screen.getByTestId("state").textContent).toBe(`authed/${TARGET}`),
		);

		refreshQueue.push(() => new HttpResponse(null, { status: 503 }));
		await act(async () => {
			await expect(exit()).rejects.toThrow(/could not restore/i);
		});

		expect(screen.getByTestId("state").textContent).toBe(`authed/${TARGET}`);
		expect(getToken()).toBe(impersonationToken);
		expect(logoutCalls).toBe(0);
	});
});
