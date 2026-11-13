import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, type NarrowedSessionEnd, readKeptNarrowedSession, useAuth } from "../auth";
import { getToken, isNarrowed, refreshToken, setRefreshHandler, setToken } from "../connect/token-store";

// A narrowed session (a team cast, a role view) is access-only: it has no
// refresh half, so a boot that exchanges the refresh cookie installs the
// PERSON's own session and silently widens them back to everything they hold.
// Before this, a reload or a hard navigation did exactly that, and the only
// thing kept across the boot was the name of what had ended. Now the tab keeps
// the bearer itself (per tab, for the tab's life) and boots into it while it is
// live; stopping, signing out, or the credential lapsing ends that.
//
// Red before: "a cast survives a reload" — the boot installed FULL and said
// the reload had ended the cast.

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims))}.s`;
const now = () => Math.floor(Date.now() / 1000);
const FULL = jwt({ sub: "user-1", who: "full", exp: now() + 900 });
const FULL2 = jwt({ sub: "user-1", who: "full-2", exp: now() + 900 });
const KEY = "codefly_narrowed_session";

const handles: { enter: (bearer: string, as: string) => void; exit: () => Promise<void>; ended: NarrowedSessionEnd | null } =
	{ enter: () => {}, exit: async () => {}, ended: null };
function Probe() {
	const { isAuthenticated, isLoading, enterRoleView, exitRoleView, narrowedSessionEnd } = useAuth();
	useEffect(() => {
		Object.assign(handles, { enter: enterRoleView, exit: exitRoleView, ended: narrowedSessionEnd });
	});
	return (
		<span data-testid="authed">
			{isLoading ? "loading" : isAuthenticated ? "yes" : "no"}
			{narrowedSessionEnd ? ` ended:${narrowedSessionEnd.reason}:${narrowedSessionEnd.as}` : ""}
		</span>
	);
}

let exchanges = 0;
afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
	exchanges = 0;
});

function refreshAnswers(queue: string[]) {
	server.use(
		// The host's reading of the session, which the provider asks for every
		// installed token; what it says is not under test here.
		http.post("http://localhost:3000/host.session.v1.SessionService/Current", () => HttpResponse.json({})),
		http.post("/v1/auth/refresh", () => {
			exchanges += 1;
			return HttpResponse.json({ accessToken: queue.shift(), refreshToken: "r" });
		}),
	);
}

// Boots a tab: a fresh provider over whatever sessionStorage holds.
async function boot(expect_: RegExp) {
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
	await waitFor(() => expect(screen.getByTestId("authed").textContent).toMatch(expect_));
}

// Simulates a reload: the tree is torn down, the in-memory token is gone, the
// tab's sessionStorage is not.
function reload() {
	cleanup();
	setToken(null);
}

let n = 0;
async function castOpen(secondsLeft: number, as = "team Stewards"): Promise<string> {
	const bearer = jwt({ sub: "user-1", who: "cast", n: ++n, exp: now() + secondsLeft });
	refreshAnswers([FULL, FULL2]);
	await boot(/^yes$/);
	act(() => handles.enter(bearer, as));
	await waitFor(() => expect(getToken()).toBe(bearer));
	return bearer;
}

describe("a team cast across a reload", () => {
	it("survives: the tab boots into the cast it kept, exchanges no refresh cookie, and still knows the bearer is narrowed", async () => {
		const cast = await castOpen(600);
		expect(exchanges).toBe(1);
		reload();
		await boot(/^yes$/);
		expect(getToken()).toBe(cast);
		expect(isNarrowed(cast)).toBe(true);
		expect(exchanges).toBe(1);
		expect(handles.ended).toBeNull();
	});

	it("is kept for this tab only, and never without its name", async () => {
		const cast = await castOpen(600);
		expect(readKeptNarrowedSession()).toEqual({ live: { bearer: cast, as: "team Stewards", since: expect.any(Number) } });
		expect(localStorage.getItem(KEY)).toBeNull();
	});

	it("stops where the person stopped: after 'Stop acting', the next boot is their own session, with no notice", async () => {
		await castOpen(600);
		await act(async () => {
			await handles.exit();
		});
		expect(getToken()).toBe(FULL2);
		expect(sessionStorage.getItem(KEY)).toBeNull();
		reload();
		refreshAnswers([FULL]);
		await boot(/^yes$/);
		expect(getToken()).toBe(FULL);
		expect(handles.ended).toBeNull();
	});

	it("does not outlive its credential: a kept bearer past its exp is not installed, the person's own session is, and the end is said by name", async () => {
		await castOpen(-1, "team Stewards");
		reload();
		refreshAnswers([FULL]);
		await boot(/^yes ended/);
		expect(getToken()).toBe(FULL);
		expect(handles.ended).toEqual({ reason: "reloaded", as: "team Stewards" });
		expect(sessionStorage.getItem(KEY)).toBeNull();
	});

	it("once the host refuses the cast (401), a reload does not boot into it again, but still says it ended", async () => {
		const cast = await castOpen(600);
		server.use(
			http.post("http://localhost:3000/host.session.v1.SessionService/Current", () =>
				HttpResponse.json({ code: "unauthenticated", message: "session expired" }, { status: 401 }),
			),
		);
		await act(async () => {
			await refreshToken(cast);
		});
		await waitFor(() => expect(handles.ended).toEqual({ reason: "expired", as: "team Stewards" }));
		expect(readKeptNarrowedSession()).toEqual({ lapsed: "team Stewards" });
		reload();
		refreshAnswers([FULL]);
		await boot(/^yes ended/);
		expect(getToken()).toBe(FULL);
		expect(handles.ended).toEqual({ reason: "reloaded", as: "team Stewards" });
	});
});
