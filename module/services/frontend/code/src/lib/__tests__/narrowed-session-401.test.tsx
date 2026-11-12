import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, type NarrowedSessionEnd, useAuth } from "../auth";
import { authedFetch, getToken, refreshToken, setRefreshHandler, setToken } from "../connect/token-store";

// A NARROWED session, a role view or a team cast, is access-only: it has no
// refresh half, so the only token a 401 repair can produce is the person's own
// full session. A request sent as the narrowed session must never be retried
// with it. These hold that rule for EVERY such request, not the first; the
// first implementation decided from a flag that the first repair reset, so the
// second 401 from the same cast bearer was replayed as the administrator.

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims))}.s`;
const FULL = jwt({ sub: "user-1", who: "full" });
const FULL2 = jwt({ sub: "user-1", who: "full-2" });
const FULL3 = jwt({ sub: "user-1", who: "full-3" });

// The provider's handles, read off the rendered tree after each commit.
const handles: {
	enter: (bearer: string, as: string) => void;
	exit: () => Promise<void>;
	dismiss: () => void;
	ended: NarrowedSessionEnd | null;
} = {
	enter: () => {},
	exit: async () => {},
	dismiss: () => {},
	ended: null,
};
function Probe() {
	const { isAuthenticated, isLoading, enterRoleView, exitRoleView, dismissNarrowedSessionEnd, narrowedSessionEnd } =
		useAuth();
	useEffect(() => {
		Object.assign(handles, {
			enter: enterRoleView,
			exit: exitRoleView,
			dismiss: dismissNarrowedSessionEnd,
			ended: narrowedSessionEnd,
		});
	});
	return (
		<span data-testid="authed">
			{isLoading ? "loading" : isAuthenticated ? "yes" : "no"}
			{narrowedSessionEnd ? ` ended:${narrowedSessionEnd.reason}:${narrowedSessionEnd.as}` : ""}
		</span>
	);
}

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

const refreshAnswers = (queue: string[]) =>
	server.use(http.post("/v1/auth/refresh", () => HttpResponse.json({ accessToken: queue.shift(), refreshToken: "r" })));

// The host's session interface: it answers every bearer it was not told to
// refuse, and records which bearer each reading carried. A narrowed session
// ends only when the host refuses its bearer here.
const CURRENT = "http://localhost:3000/host.session.v1.SessionService/Current";
let refusedByHost = new Set<string>();
let readFor: string[] = [];
function hostSession() {
	refusedByHost = new Set();
	readFor = [];
	server.use(
		http.post(CURRENT, ({ request }) => {
			const bearer = (request.headers.get("authorization") ?? "").replace(/^Bearer /, "");
			readFor.push(bearer);
			if (refusedByHost.has(bearer)) {
				return HttpResponse.json({ code: "unauthenticated", message: "session expired" }, { status: 401 });
			}
			return HttpResponse.json({});
		}),
	);
}

async function boot() {
	hostSession();
	refreshAnswers([FULL, FULL2, FULL3]);
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
	await waitFor(() => expect(screen.getByTestId("authed").textContent).toMatch(/^yes/));
}

// Every narrowed bearer is distinct across tests: the store remembers them
// for the life of the page, which here is the test file.
let n = 0;
async function narrowedTo(as: string): Promise<string> {
	const bearer = jwt({ sub: "user-1", who: "narrowed", n: ++n });
	await boot();
	act(() => handles.enter(bearer, as));
	await waitFor(() => expect(getToken()).toBe(bearer));
	// The host read the session it was entered as, and accepted it.
	await waitFor(() => expect(readFor).toContain(bearer));
	return bearer;
}

// A backend that refuses the narrowed bearer and accepts any other, recording
// which bearer each attempt carried. The narrowed credential lapsed, so the
// host refuses it too.
function backend(narrowed: string, gate: Partial<Record<string, Promise<void>>> = {}) {
	refusedByHost.add(narrowed);
	const seen: Record<string, string[]> = { a: [], b: [] };
	for (const path of ["a", "b"]) {
		server.use(
			http.post(`/write/${path}`, async ({ request }) => {
				const bearer = request.headers.get("authorization") ?? "";
				seen[path].push(bearer);
				if (bearer !== `Bearer ${narrowed}`) return HttpResponse.json({ ok: true });
				await gate[path];
				return new HttpResponse(null, { status: 401 });
			}),
		);
	}
	return seen;
}

describe.each([
	["a team cast", "team Stewards"],
	["a role view", "viewer on records"],
])("inside %s", (_, as) => {
	it("the first 401 is not repaired with the full session, and the end is said by name", async () => {
		const narrowed = await narrowedTo(as);
		refusedByHost.add(narrowed);
		let fresh: string | null = "unset";
		await act(async () => {
			fresh = await refreshToken();
		});
		expect(fresh).toBeNull();
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe(`yes ended:expired:${as}`));
	});

	// A module the narrowed session reached refuses one call (a page's own
	// check, its binding's) while the host still accepts the bearer: that call
	// fails with the module's own refusal, and the session goes on. Red before:
	// any 401 under a narrowed bearer was read as its credential expiring, so a
	// page refusing one action ended the whole cast.
	it("a module's 401 keeps the session and hands the caller the module's refusal", async () => {
		const narrowed = await narrowedTo(as);
		server.use(
			http.post("/s/records/api/purge", () =>
				HttpResponse.json({ code: "unauthenticated", message: "records: purge needs the editor role" }, { status: 401 }),
			),
		);
		let res: Response | undefined;
		await act(async () => {
			res = await authedFetch("/s/records/api/purge", { method: "POST" });
		});
		expect(res?.status).toBe(401);
		expect(await res?.json()).toEqual({ code: "unauthenticated", message: "records: purge needs the editor role" });
		// The host was asked about the bearer itself, and accepted it.
		await waitFor(() => expect(readFor.filter((b) => b === narrowed).length).toBeGreaterThanOrEqual(2));
		await act(async () => {});
		expect(getToken()).toBe(narrowed);
		expect(handles.ended).toBeNull();
		expect(screen.getByTestId("authed").textContent).toBe("yes");
	});

	it("the host's refusal of the bearer itself ends it, by name", async () => {
		const narrowed = await narrowedTo(as);
		server.use(http.post("/s/records/api/purge", () => new HttpResponse(null, { status: 401 })));
		refusedByHost.add(narrowed);
		await act(async () => {
			await authedFetch("/s/records/api/purge", { method: "POST" });
		});
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe(`yes ended:expired:${as}`));
		expect(getToken()).toBe(narrowed);
	});

	it("a second request issued under it, whose 401 lands after the first repair settled, is not replayed either", async () => {
		const narrowed = await narrowedTo(as);
		let releaseB: () => void = () => {};
		const seen = backend(narrowed, { b: new Promise<void>((r) => (releaseB = r)) });
		let a: Response | undefined;
		let b: Response | undefined;
		await act(async () => {
			const pa = authedFetch("/write/a", { method: "POST" });
			const pb = authedFetch("/write/b", { method: "POST" }); // issued as the narrowed session, slower to answer
			a = await pa; // the first 401 and whatever repair it triggers settle here
			releaseB();
			b = await pb;
		});
		expect({ a: a?.status, attempts: seen.a }).toEqual({ a: 401, attempts: [`Bearer ${narrowed}`] });
		expect({ b: b?.status, attempts: seen.b }).toEqual({ b: 401, attempts: [`Bearer ${narrowed}`] });
		// Nothing installed the person's own session behind their back.
		expect(getToken()).toBe(narrowed);
		await waitFor(() => expect(handles.ended).toEqual({ reason: "expired", as }));
	});
});

it("after the person returns to their own session, a late 401 from a request sent as the cast is still not replayed as them", async () => {
	const cast = await narrowedTo("team Stewards");
	let release: () => void = () => {};
	const seen = backend(cast, { a: new Promise<void>((r) => (release = r)) });
	let a: Response | undefined;
	await act(async () => {
		const pa = authedFetch("/write/a", { method: "POST" }); // sent as the cast
		await handles.exit(); // the person asks for their own session back while it is in flight
		expect(getToken()).toBe(FULL2);
		release();
		a = await pa;
	});
	expect({ status: a?.status, attempts: seen.a }).toEqual({ status: 401, attempts: [`Bearer ${cast}`] });
	// And a session the person asked for is not reported as an ended cast.
	expect(handles.ended).toBeNull();
});

it("an expired cast is left until the person asks: returning installs their own session and clears the notice", async () => {
	const cast = await narrowedTo("team Stewards");
	backend(cast);
	await act(async () => {
		await authedFetch("/write/a", { method: "POST" });
	});
	expect(getToken()).toBe(cast);
	await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes ended:expired:team Stewards"));
	await act(async () => {
		await handles.exit();
	});
	expect(getToken()).toBe(FULL2);
	expect(screen.getByTestId("authed").textContent).toBe("yes");
});

it("a reload after the narrowed bearer lapsed ends the session, and the boot says so by name; acknowledging clears it", async () => {
	sessionStorage.setItem("codefly_narrowed_session", JSON.stringify({ as: "team Stewards" }));
	await boot();
	expect(screen.getByTestId("authed").textContent).toBe("yes ended:reloaded:team Stewards");
	expect(sessionStorage.getItem("codefly_narrowed_session")).toBeNull();
	act(() => handles.dismiss());
	expect(screen.getByTestId("authed").textContent).toBe("yes");
});

it("a request sent as the person BEFORE the cast, whose 401 lands inside it, neither replaces the cast nor is replayed", async () => {
	refreshAnswers([FULL, FULL2]);
	let release: () => void = () => {};
	const gate = new Promise<void>((r) => (release = r));
	const seen: string[] = [];
	server.use(
		http.get("/slow", async ({ request }) => {
			seen.push(request.headers.get("authorization") ?? "");
			if (request.headers.get("authorization") === `Bearer ${FULL}`) {
				await gate;
				return new HttpResponse(null, { status: 401 });
			}
			return HttpResponse.json({ ok: true });
		}),
	);
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
	await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes"));
	const cast = jwt({ sub: "user-1", who: "narrowed", n: ++n });
	let res: Response | undefined;
	await act(async () => {
		const p = authedFetch("/slow"); // sent as the person, before the cast
		handles.enter(cast, "team Stewards"); // the person opens a cast
		expect(getToken()).toBe(cast);
		release(); // the old request now 401s
		res = await p;
	});
	// The cast the person chose stays installed, nothing was exchanged or
	// installed behind their back, and the pre-cast request is not replayed.
	expect({ installed: getToken() === cast ? "cast" : "other", notice: handles.ended, replayed: res?.status, attempts: seen }).toEqual({
		installed: "cast",
		notice: null,
		replayed: 401,
		attempts: [`Bearer ${FULL}`],
	});
	expect(screen.getByTestId("authed").textContent).toBe("yes");
});

it("a 401 of the person's own session is still repaired, and says no narrowed session ended", async () => {
	await boot();
	let fresh: string | null = null;
	await act(async () => {
		fresh = await refreshToken();
	});
	expect(fresh).toBe(FULL2);
	expect(handles.ended).toBeNull();
});
