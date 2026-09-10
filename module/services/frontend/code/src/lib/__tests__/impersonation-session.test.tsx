import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, useAuth } from "../auth";
import { getToken, setRefreshHandler, setToken } from "../connect/token-store";

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
});
