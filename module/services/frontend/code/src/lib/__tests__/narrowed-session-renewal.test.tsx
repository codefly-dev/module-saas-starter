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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
// Its life is timed on the tab's own clock from when it was installed, so a
// host whose clock disagrees changes nothing.
//
// Red before: "an active cast is opened again" — nothing reopened it, and the
// tab stayed on the bearer that was about to lapse.

const jwt = (claims: Record<string, unknown>) =>
	`h.${btoa(JSON.stringify(claims))}.s`;
const now = () => Math.floor(Date.now() / 1000);
const FULL = jwt({ sub: "user-1", who: "full", exp: now() + 3600 });
const FULL2 = jwt({ sub: "user-1", who: "full-2", exp: now() + 3600 });
const FULL3 = jwt({ sub: "user-1", who: "full-3", exp: now() + 3600 });
const HOST = "http://localhost:3000/host.session.v1.SessionService";
const TEAM = "team-stewards";
const MINUTE = 60_000;

let n = 0;
/** A five-minute cast bearer, as a host whose clock is `skew` seconds off this tab's mints it. */
const castBearer = (skew = 0) =>
	jwt({
		sub: "user-1",
		who: "cast",
		n: ++n,
		iat: now() + skew,
		exp: now() + skew + 300,
	});

/** The person's own input: trusted, as a browser marks it. */
function input(type: "pointerdown" | "keydown" = "pointerdown") {
	const event = new Event(type, { bubbles: true });
	Object.defineProperty(event, "isTrusted", { value: true });
	act(() => {
		document.body.dispatchEvent(event);
	});
}
const advance = (ms: number) =>
	act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
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
// recorded with the bearer it was asked from, and answered with `next`, or
// with `openAnswer`'s status, after `openHeld` settles.
let refreshQueue: string[] = [];
let opens: { method: string; body: Record<string, unknown>; from: string }[] =
	[];
let next: string[] = [];
let openAnswer: number[] = [];
let openHeld: Promise<void> = Promise.resolve();
let refused = new Set<string>();
let reading: Record<string, unknown> = {};
const installed: string[] = [];
const recordInstalled = () => installed.push(getToken() ?? "");

beforeEach(() => {
	vi.useFakeTimers({
		shouldAdvanceTime: true,
		toFake: ["setTimeout", "clearTimeout", "Date", "performance"],
	});
	refreshQueue = [FULL, FULL2, FULL3];
	opens = [];
	next = [];
	openAnswer = [];
	openHeld = Promise.resolve();
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
			await openHeld;
			const status = openAnswer.shift();
			if (status === 403)
				return HttpResponse.json(
					{ code: "permission_denied", message: "no longer grantable" },
					{ status },
				);
			if (status === 503)
				return HttpResponse.json(
					{ code: "unavailable", message: "try again" },
					{ status },
				);
			return HttpResponse.json({ bearer: next.shift(), ...reading });
		}),
	);
});

afterEach(() => {
	window.removeEventListener("codefly:auth-changed", recordInstalled);
	cleanup();
	vi.useRealTimers();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

async function boot(tree = <Probe />) {
	render(
		<AuthProvider>
			{tree}
			<Page />
		</AuthProvider>,
	);
	await waitFor(() =>
		expect(screen.getByTestId("authed").textContent).toMatch(/^yes/),
	);
}

async function openCast(skew = 0): Promise<string> {
	await boot();
	const cast = castBearer(skew);
	act(() => handles.enter(cast, "team Stewards"));
	// Armed once the host has said what the session is.
	await waitFor(() =>
		expect(screen.getByTestId("authed").textContent).toBe(`yes cast:${TEAM}`),
	);
	return cast;
}

describe("a narrowed session the person is using", () => {
	it("an active cast is opened again: the same team, from the person's own session, before it lapses, without remounting the page", async () => {
		await openCast();
		const renewed = castBearer();
		next.push(renewed);
		input();
		await advance(4 * MINUTE + 1000);
		await waitFor(() => expect(getToken()).toBe(renewed));
		expect(opens).toEqual([
			{ method: "OpenTeamCast", body: { teamId: TEAM }, from: FULL2 },
		]);
		expect(isNarrowed(renewed)).toBe(true);
		expect(readKeptNarrowedSession().live).toMatchObject({
			bearer: renewed,
			as: "team Stewards",
		});
		// The person's own session was used to ask and was never this tab's.
		expect(installed).not.toContain(FULL2);
		expect(screen.getByTestId("authed").textContent).toBe(`yes cast:${TEAM}`);
		expect(mounts).toBe(1);
		expect(handles.ended).toBeNull();
	});

	it("input in the last minute opens it again then", async () => {
		await openCast();
		next.push(castBearer());
		await advance(4 * MINUTE + 5000);
		expect(opens).toEqual([]);
		input("keydown");
		await waitFor(() => expect(opens).toHaveLength(1));
	});

	it("a role view on one installation is opened again with its roles", async () => {
		reading = { roleView: [{ installationId: "inst-1", roles: ["editor"] }] };
		await boot();
		act(() => handles.enter(castBearer(), "editor"));
		const renewed = castBearer();
		next.push(renewed);
		await advance(100);
		input();
		await advance(4 * MINUTE + 1000);
		await waitFor(() => expect(getToken()).toBe(renewed));
		expect(opens).toEqual([
			{
				method: "OpenRoleView",
				body: { installationId: "inst-1", roles: ["editor"] },
				from: FULL2,
			},
		]);
	});

	it("survives a reload on its own clock: the age it had before is kept", async () => {
		await openCast();
		await advance(3 * MINUTE);
		cleanup();
		setToken(null);
		await boot();
		await waitFor(() =>
			expect(screen.getByTestId("authed").textContent).toBe(`yes cast:${TEAM}`),
		);
		next.push(castBearer());
		input();
		await advance(MINUTE + 1000);
		await waitFor(() => expect(opens).toHaveLength(1));
	});
});

describe("a narrowed session nobody is using", () => {
	it("is not opened again, and input after it lapsed brings nothing back; the host's refusal ends it by name", async () => {
		const cast = await openCast();
		await advance(5 * MINUTE + 1000);
		input();
		await advance(100);
		expect(opens).toEqual([]);
		expect(getToken()).toBe(cast);

		refused.add(cast);
		await act(async () => {
			await refreshToken(cast);
		});
		await waitFor(() =>
			expect(handles.ended).toEqual({ reason: "expired", as: "team Stewards" }),
		);
	});

	it("an untrusted (script) event is not the person's input", async () => {
		await openCast();
		fireEvent.pointerDown(document.body);
		window.dispatchEvent(new Event("keydown"));
		await advance(4 * MINUTE + 1000);
		expect(opens).toEqual([]);
	});

	it("after one renewal, an idle second cycle lapses: the renewed bearer starts with no input", async () => {
		await openCast();
		const renewed = castBearer();
		next.push(renewed, castBearer());
		input();
		await advance(4 * MINUTE + 1000);
		await waitFor(() => expect(getToken()).toBe(renewed));
		await advance(5 * MINUTE + 1000);
		expect(opens).toHaveLength(1);
		expect(getToken()).toBe(renewed);
	});
});

describe("an end is never undone", () => {
	it("a reopen that returns after the host said the session ended installs nothing and keeps the notice", async () => {
		const cast = await openCast();
		next.push(castBearer());
		let release = () => {};
		openHeld = new Promise((resolve) => {
			release = resolve;
		});
		input();
		await advance(4 * MINUTE + 1000);
		await waitFor(() => expect(opens).toHaveLength(1));

		refused.add(cast);
		await act(async () => {
			await refreshToken(cast);
		});
		await waitFor(() =>
			expect(handles.ended).toEqual({ reason: "expired", as: "team Stewards" }),
		);
		release();
		await advance(100);
		expect(getToken()).toBe(cast);
		expect(handles.ended).toEqual({ reason: "expired", as: "team Stewards" });
		expect(readKeptNarrowedSession()).toEqual({ lapsed: "team Stewards" });
	});

	it("a host clock ahead of this tab's never stretches the life: past five minutes here, nothing reopens", async () => {
		// Read on the host's clock, this bearer would have minutes left after
		// its five here are over, and its "last minute" would come after that.
		await openCast(240);
		next.push(castBearer());
		await advance(5 * MINUTE + 1000);
		input();
		await advance(3 * MINUTE);
		input();
		await advance(100);
		expect(opens).toEqual([]);
	});

	it("a host clock behind this tab's does not reopen on the first input", async () => {
		// Read on the host's clock, this bearer would already be in its last minute.
		await openCast(-270);
		next.push(castBearer());
		input();
		await advance(1000);
		expect(opens).toEqual([]);
		await advance(4 * MINUTE);
		await waitFor(() => expect(opens).toHaveLength(1));
	});

	it("the host's refusal of a reopen is final: no more asks, and the person's own session is never installed", async () => {
		const cast = await openCast();
		openAnswer.push(403);
		input();
		await advance(4 * MINUTE + 1000);
		await waitFor(() => expect(opens).toHaveLength(1));
		await advance(10_000);
		input();
		await advance(100);
		expect(opens).toHaveLength(1);
		expect(getToken()).toBe(cast);
		expect(installed).not.toContain(FULL2);
	});

	it("a host that could not answer is asked again on the next input", async () => {
		await openCast();
		const renewed = castBearer();
		openAnswer.push(503);
		next.push(renewed);
		input();
		await advance(4 * MINUTE + 1000);
		await waitFor(() => expect(opens).toHaveLength(1));
		await advance(6000);
		input();
		await waitFor(() => expect(getToken()).toBe(renewed));
		expect(opens).toHaveLength(2);
	});
});
