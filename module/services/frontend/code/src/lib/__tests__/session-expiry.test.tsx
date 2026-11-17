import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, readKeptNarrowedSession, useAuth } from "../auth";
import { getToken, setRefreshHandler, setToken } from "../connect/token-store";

// The host states when a session ends on its own (Session.expires_at_unix):
// from then on its bearer is unauthenticated wherever a session is read. This
// tab holds a bearer no longer than that: a narrowed session past its end is
// said to have ended, by name, and is not booted into again; the person's own
// session past its end is asked of the host again and, refused, is signed out.
// A host that states no end (0) changes nothing.

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims))}.s`;
const inAnHour = () => Math.floor(Date.now() / 1000) + 3600;
const OWN = jwt({ sub: "user-1", who: "own" });
const OWN2 = jwt({ sub: "user-1", who: "own-2" });

const handles: { enter: (bearer: string, as: string) => void } = { enter: () => {} };
function Probe() {
	const { isAuthenticated, isLoading, enterRoleView, narrowedSessionEnd } = useAuth();
	useEffect(() => {
		Object.assign(handles, { enter: enterRoleView });
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
	vi.restoreAllMocks();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

// The host: each bearer's session ends when it was told (Unix seconds, as the
// wire carries an int64), or at no stated moment.
let endsAt = new Map<string, number>();
let refreshes: string[] = [];
let refreshed = 0;
function host(queue: string[]) {
	endsAt = new Map();
	refreshes = queue;
	refreshed = 0;
	server.use(
		http.post("/v1/auth/refresh", () => {
			refreshed++;
			const accessToken = refreshes.shift();
			if (!accessToken) return new HttpResponse(null, { status: 401 });
			return HttpResponse.json({ accessToken, refreshToken: "r" });
		}),
		http.post("http://localhost:3000/host.session.v1.SessionService/Current", ({ request }) => {
			const bearer = (request.headers.get("authorization") ?? "").replace(/^Bearer /, "");
			const at = endsAt.get(bearer);
			return HttpResponse.json(at ? { expiresAtUnix: String(at) } : {});
		}),
	);
}

function mount() {
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
}

const settle = () => act(() => new Promise<void>((resolve) => setTimeout(resolve, 150)));

describe("a narrowed session past the end the host stated", () => {
	it("is said to have ended, by name, and its bearer is not kept for the next boot", async () => {
		host([OWN]);
		const cast = jwt({ sub: "user-1", who: "cast", exp: inAnHour(), n: 1 });
		endsAt.set(cast, Math.floor(Date.now() / 1000) - 1);
		mount();
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes"));
		act(() => handles.enter(cast, "team Stewards"));
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes ended:expired:team Stewards"));
		// The person's own session is not installed for them: they ask for it.
		expect(refreshed).toBe(1);
		expect(readKeptNarrowedSession()).toEqual({ lapsed: "team Stewards" });
	});

	it("is not booted into again, though its token's own window is still open", () => {
		const cast = jwt({ sub: "user-1", who: "cast", exp: inAnHour(), n: 2 });
		sessionStorage.setItem(
			"codefly_narrowed_session",
			JSON.stringify({ bearer: cast, as: "team Stewards", expiresAt: Math.floor(Date.now() / 1000) - 1 }),
		);
		expect(readKeptNarrowedSession()).toEqual({ lapsed: "team Stewards" });
	});

	it("keeps the end the host stated beside the bearer, so a reload holds it no longer", async () => {
		host([OWN]);
		const cast = jwt({ sub: "user-1", who: "cast", exp: inAnHour(), n: 3 });
		const at = inAnHour();
		endsAt.set(cast, at);
		mount();
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes"));
		act(() => handles.enter(cast, "team Stewards"));
		await waitFor(() =>
			expect(JSON.parse(sessionStorage.getItem("codefly_narrowed_session") ?? "{}").expiresAt).toBe(at),
		);
		await settle();
		// Its end has not come: it is the session, with nothing said.
		expect(getToken()).toBe(cast);
		expect(screen.getByTestId("authed").textContent).toBe("yes");
	});
});

describe("the person's own session past the end the host stated", () => {
	it("is asked of the host again, and signed out when the host refuses it", async () => {
		const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {});
		host([OWN]);
		endsAt.set(OWN, Math.floor(Date.now() / 1000) - 1);
		mount();
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("no"));
		// One exchange booted the session, one asked for it past its end.
		expect(refreshed).toBe(2);
		expect(getToken()).toBeNull();
		expect(replace).toHaveBeenCalled();
	});

	it("continues as the session the host opens in its place", async () => {
		host([OWN, OWN2]);
		endsAt.set(OWN, Math.floor(Date.now() / 1000) - 1);
		endsAt.set(OWN2, inAnHour());
		mount();
		await waitFor(() => expect(getToken()).toBe(OWN2));
		await settle();
		expect(screen.getByTestId("authed").textContent).toBe("yes");
		expect(refreshed).toBe(2);
	});

	it("is kept as it is when the host states no end", async () => {
		host([OWN, OWN2]);
		mount();
		await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes"));
		await settle();
		expect(getToken()).toBe(OWN);
		expect(refreshed).toBe(1);
	});
});
