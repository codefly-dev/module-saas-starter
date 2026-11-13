import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect, useState } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import {
	AuthProvider,
	type NarrowedSessionEnd,
	readKeptNarrowedSession,
	useAuth,
} from "../auth";
import {
	getToken,
	isNarrowed,
	refreshToken,
	setRefreshHandler,
	setToken,
} from "../connect/token-store";

// A narrowed session (a team cast, a role view) is access-only and capped at a
// few minutes, so a forgotten tab cannot keep acting as a team. Before this, a
// person USING the cast was cut off at the cap too: a directory import (extract,
// review, save) lost its session mid-flow, again and again. Now an active tab
// asks the host for the same cast again, from the person's own session, just
// before it lapses; an idle one asks for nothing and lapses exactly as before.
//
// Red before: "an active cast is opened again" — nothing reopened it, and the
// tab stayed on the bearer that was about to lapse.

const jwt = (claims: Record<string, unknown>) =>
	`h.${btoa(JSON.stringify(claims))}.s`;
const now = () => Math.floor(Date.now() / 1000);
const FULL = jwt({ sub: "user-1", who: "full", exp: now() + 900 });
const FULL2 = jwt({ sub: "user-1", who: "full-2", exp: now() + 900 });
const HOST = "http://localhost:3000/host.session.v1.SessionService";
const TEAM = "team-stewards";

let n = 0;
/** A cast bearer five minutes long, `secondsLeft` from lapsing. */
const castBearer = (secondsLeft: number) =>
	jwt({
		sub: "user-1",
		who: "cast",
		n: ++n,
		iat: now() + secondsLeft - 300,
		exp: now() + secondsLeft,
	});

const handles: {
	enter: (bearer: string, as: string) => void;
	ended: NarrowedSessionEnd | null;
} = {
	enter: () => {},
	ended: null,
};
let mounts = 0;
function Probe() {
	const {
		isAuthenticated,
		isLoading,
		enterRoleView,
		narrowedSessionEnd,
		castTeamId,
	} = useAuth();
	useEffect(() => {
		Object.assign(handles, { enter: enterRoleView, ended: narrowedSessionEnd });
	});
	return (
		<span data-testid="authed">
			{isLoading ? "loading" : isAuthenticated ? "yes" : "no"}
			{castTeamId ? ` cast:${castTeamId}` : ""}
		</span>
	);
}
// A page mid-flow: what it holds lives in its own state, which a remount drops.
function Page() {
	const [draft] = useState(() => {
		mounts += 1;
		return "extracted rows";
	});
	return <span data-testid="page">{draft}</span>;
}

// The host: the refresh cookie answers the person's own session in turn; a
// reading names the cast or role view a narrowed bearer is; each open is
// recorded with the bearer it was asked from, and answered with `next`.
let refreshQueue: string[] = [];
let opens: { method: string; body: Record<string, unknown>; from: string }[] =
	[];
let next: string[] = [];
let refuseOpens = false;
let refused = new Set<string>();
let reading: Record<string, unknown> = {};
const installed: string[] = [];
const recordInstalled = () => installed.push(getToken() ?? "");

beforeEach(() => {
	refreshQueue = [FULL, FULL2];
	opens = [];
	next = [];
	refuseOpens = false;
	refused = new Set();
	reading = { castTeamId: TEAM };
	installed.length = 0;
	mounts = 0;
	window.addEventListener("codefly:auth-changed", recordInstalled);
	server.use(
		http.post("/v1/auth/refresh", () =>
			HttpResponse.json({
				accessToken: refreshQueue.shift(),
				refreshToken: "r",
			}),
		),
		http.post(`${HOST}/Current`, ({ request }) => {
			const bearer = (request.headers.get("authorization") ?? "").replace(
				/^Bearer /,
				"",
			);
			if (refused.has(bearer))
				return HttpResponse.json(
					{ code: "unauthenticated", message: "session expired" },
					{ status: 401 },
				);
			return HttpResponse.json(isNarrowed(bearer) ? reading : {});
		}),
		http.post(`${HOST}/:method`, async ({ request, params }) => {
			const method = String(params.method);
			if (method !== "OpenTeamCast" && method !== "OpenRoleView") return;
			opens.push({
				method,
				body: (await request.json()) as Record<string, unknown>,
				from: (request.headers.get("authorization") ?? "").replace(
					/^Bearer /,
					"",
				),
			});
			if (refuseOpens)
				return HttpResponse.json(
					{ code: "permission_denied", message: "no longer grantable" },
					{ status: 403 },
				);
			return HttpResponse.json({ bearer: next.shift(), ...reading });
		}),
	);
});

afterEach(() => {
	window.removeEventListener("codefly:auth-changed", recordInstalled);
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

async function openCast(secondsLeft: number): Promise<string> {
	render(
		<AuthProvider>
			<Probe />
			<Page />
		</AuthProvider>,
	);
	await waitFor(() =>
		expect(screen.getByTestId("authed").textContent).toBe("yes"),
	);
	const cast = castBearer(secondsLeft);
	act(() => handles.enter(cast, "team Stewards"));
	// Armed once the host has said what the session is.
	await waitFor(() =>
		expect(screen.getByTestId("authed").textContent).toBe(`yes cast:${TEAM}`),
	);
	return cast;
}

const pause = (ms: number) =>
	act(() => new Promise((resolve) => setTimeout(resolve, ms)));

describe("a narrowed session the person is using", () => {
	it("an active cast is opened again: the same team, from the person's own session, before it lapses, without remounting the page", async () => {
		await openCast(30);
		const renewed = castBearer(300);
		next.push(renewed);

		fireEvent.pointerDown(document.body);

		await waitFor(() => expect(getToken()).toBe(renewed));
		expect(opens).toEqual([
			{ method: "OpenTeamCast", body: { teamId: TEAM }, from: FULL2 },
		]);
		expect(isNarrowed(renewed)).toBe(true);
		expect(readKeptNarrowedSession()).toEqual({
			live: { bearer: renewed, as: "team Stewards" },
		});
		// The person's own session was used to ask and was never this tab's.
		expect(installed).not.toContain(FULL2);
		expect(screen.getByTestId("authed").textContent).toBe(`yes cast:${TEAM}`);
		expect(mounts).toBe(1);
		expect(screen.getByTestId("page").textContent).toBe("extracted rows");
		expect(handles.ended).toBeNull();
	});

	it("input before the last minute counts: the cast is opened again when that minute starts", async () => {
		await openCast(61);
		const renewed = castBearer(300);
		next.push(renewed);
		fireEvent.keyDown(document.body, { key: "a" });
		expect(opens).toEqual([]);
		await waitFor(() => expect(getToken()).toBe(renewed), { timeout: 3000 });
		expect(opens).toHaveLength(1);
	});

	it("a role view on one installation is opened again with its roles", async () => {
		reading = { roleView: [{ installationId: "inst-1", roles: ["editor"] }] };
		render(
			<AuthProvider>
				<Probe />
			</AuthProvider>,
		);
		await waitFor(() =>
			expect(screen.getByTestId("authed").textContent).toBe("yes"),
		);
		const view = castBearer(30);
		act(() => handles.enter(view, "editor"));
		const renewed = castBearer(300);
		next.push(renewed);
		await pause(50);
		fireEvent.pointerDown(document.body);
		await waitFor(() => expect(getToken()).toBe(renewed));
		expect(opens).toEqual([
			{
				method: "OpenRoleView",
				body: { installationId: "inst-1", roles: ["editor"] },
				from: FULL2,
			},
		]);
	});
});

describe("a narrowed session nobody is using", () => {
	it("is not opened again when its last minute comes", async () => {
		const cast = await openCast(61);
		await pause(1500);
		expect(opens).toEqual([]);
		expect(getToken()).toBe(cast);
	});

	it("lapses: input after its exp brings nothing back, and the host's refusal ends it by name", async () => {
		const cast = await openCast(2);
		await pause(2500);
		fireEvent.pointerDown(document.body);
		await pause(50);
		expect(opens).toEqual([]);
		expect(getToken()).toBe(cast);

		refused.add(cast);
		await act(async () => {
			await refreshToken(cast);
		});
		await waitFor(() =>
			expect(handles.ended).toEqual({ reason: "expired", as: "team Stewards" }),
		);
		expect(getToken()).toBe(cast);
	});

	it("a cast the host will no longer open is left to lapse, and the person's own session is never installed", async () => {
		const cast = await openCast(30);
		refuseOpens = true;
		fireEvent.pointerDown(document.body);
		await waitFor(() => expect(opens).toHaveLength(1));
		await pause(50);
		expect(getToken()).toBe(cast);
		expect(installed).not.toContain(FULL2);
	});
});
