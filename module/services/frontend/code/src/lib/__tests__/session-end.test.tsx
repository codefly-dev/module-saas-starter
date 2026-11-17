import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, type SignOutResult, useAuth } from "../auth";
import { getToken, setRefreshHandler, setToken } from "../connect/token-store";

// The host's session interface ends a session and every session opened from
// it (its role views and casts) on SessionService.End, under that session's
// own bearer. `/v1/auth/logout` revokes the refresh family and the presented
// session's access tokens only: a cast is a family of its own, so without End
// a sign-out left every cast opened from the session live until its own end.
// These hold that a sign-out ends the person's OWN session through End, that a
// narrowed session being left is ended under its own bearer, and that an End
// the host could not perform is never presented as a finished sign-out.

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims))}.s`;
const OWN = jwt({ sub: "user-1", who: "own" });
const OWN2 = jwt({ sub: "user-1", who: "own-2" });
const OWN3 = jwt({ sub: "user-1", who: "own-3" });

const handles: {
	logout: () => Promise<SignOutResult>;
	enter: (bearer: string, as: string) => void;
	exit: () => Promise<void>;
} = {
	logout: async () => ({ complete: false }),
	enter: () => {},
	exit: async () => {},
};
function Probe() {
	const { isAuthenticated, isLoading, logout, enterRoleView, exitRoleView } = useAuth();
	useEffect(() => {
		Object.assign(handles, { logout, enter: enterRoleView, exit: exitRoleView });
	});
	return <span data-testid="authed">{isLoading ? "loading" : isAuthenticated ? "yes" : "no"}</span>;
}

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

const bearerOf = (request: Request) => (request.headers.get("authorization") ?? "").replace(/^Bearer /, "");

// The host: every call it received, in order, with the bearer it carried; an
// End it was told to fail answers Unavailable, one it was told the bearer is
// no longer a session for answers Unauthenticated.
let calls: string[] = [];
let endUnavailableFor = new Set<string>();
let endUnauthenticatedFor = new Set<string>();
let logoutStatus = 200;
function host(refreshes: string[]) {
	calls = [];
	endUnavailableFor = new Set();
	endUnauthenticatedFor = new Set();
	logoutStatus = 200;
	server.use(
		http.post("/v1/auth/refresh", () => {
			const accessToken = refreshes.shift();
			if (!accessToken) return new HttpResponse(null, { status: 401 });
			calls.push(`refresh ${accessToken}`);
			return HttpResponse.json({ accessToken, refreshToken: "r" });
		}),
		http.post("http://localhost:3000/host.session.v1.SessionService/Current", () => HttpResponse.json({})),
		http.post("http://localhost:3000/host.session.v1.SessionService/End", ({ request }) => {
			const bearer = bearerOf(request);
			calls.push(`end ${bearer}`);
			if (endUnavailableFor.has(bearer))
				return HttpResponse.json({ code: "unavailable", message: "the session could not be ended" }, { status: 503 });
			if (endUnauthenticatedFor.has(bearer))
				return HttpResponse.json({ code: "unauthenticated", message: "no open session" }, { status: 401 });
			return HttpResponse.json({});
		}),
		http.post("/v1/auth/logout", ({ request }) => {
			calls.push(`logout ${bearerOf(request)}`);
			return new HttpResponse(logoutStatus === 200 ? "{}" : "logout incomplete, retry", { status: logoutStatus });
		}),
	);
}

async function boot(refreshes: string[]) {
	host(refreshes);
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
	await waitFor(() => expect(screen.getByTestId("authed").textContent).toBe("yes"));
}

let n = 0;
async function castFrom(refreshes: string[]): Promise<string> {
	const cast = jwt({ sub: "user-1", who: "cast", n: ++n });
	await boot(refreshes);
	act(() => handles.enter(cast, "team Stewards"));
	await waitFor(() => expect(getToken()).toBe(cast));
	return cast;
}

async function signOut(): Promise<SignOutResult> {
	let result: SignOutResult = { complete: false };
	await act(async () => {
		result = await handles.logout();
	});
	return result;
}

describe("sign-out", () => {
	it("ends the person's own session through End, under its bearer, before the logout that clears the cookie", async () => {
		await boot([OWN]);
		const result = await signOut();
		// The ended bearer is not presented again: the logout that follows is the
		// cookie's, which is what it revokes and clears.
		expect(calls.slice(1)).toEqual([`end ${OWN}`, "logout "]);
		expect(result).toEqual({ complete: true });
		expect(screen.getByTestId("authed").textContent).toBe("no");
	});

	it("is not complete when the host could not end the session, though the logout answered", async () => {
		await boot([OWN]);
		endUnavailableFor.add(OWN);
		const result = await signOut();
		// The logout still runs, as the session it is still a bearer of.
		expect(calls.slice(1)).toEqual([`end ${OWN}`, `logout ${OWN}`]);
		expect(result).toEqual({ complete: false });
		// The person asked to sign out: this tab is signed out either way.
		expect(screen.getByTestId("authed").textContent).toBe("no");
	});

	it("is not complete when the logout fails, as before, with the session ended", async () => {
		await boot([OWN]);
		logoutStatus = 503;
		const result = await signOut();
		expect(calls.slice(1)).toEqual([`end ${OWN}`, "logout "]);
		expect(result).toEqual({ complete: false });
	});

	it("ends the session under a fresh bearer when the installed one lapsed", async () => {
		await boot([OWN, OWN2]);
		endUnauthenticatedFor.add(OWN);
		const result = await signOut();
		expect(calls.slice(1)).toEqual([`end ${OWN}`, `refresh ${OWN2}`, `end ${OWN2}`, "logout "]);
		expect(result).toEqual({ complete: true });
	});

	it("is complete when the session had already ended: there is nothing left for End to end", async () => {
		await boot([OWN]);
		endUnauthenticatedFor.add(OWN);
		const result = await signOut();
		// The refresh cookie is refused too (the queue is empty): the session is gone.
		expect(calls.slice(1)).toEqual([`end ${OWN}`, `logout ${OWN}`]);
		expect(result).toEqual({ complete: true });
	});

	it("from inside a cast, ends the person's own session, and with it the cast", async () => {
		const cast = await castFrom([OWN, OWN2]);
		const result = await signOut();
		// The cast's bearer cannot end the session it was opened from; the
		// person's own, read from the refresh cookie, ends both.
		expect(calls.slice(1)).toEqual([`refresh ${OWN2}`, `end ${OWN2}`, "logout "]);
		expect(calls).not.toContain(`end ${cast}`);
		expect(result).toEqual({ complete: true });
	});

	it("from inside a cast whose own session cannot be ended, ends the cast itself and is not complete", async () => {
		const cast = await castFrom([OWN, OWN2]);
		endUnavailableFor.add(OWN2);
		const result = await signOut();
		expect(calls.slice(1)).toEqual([`refresh ${OWN2}`, `end ${OWN2}`, `end ${cast}`, `logout ${OWN2}`]);
		expect(result).toEqual({ complete: false });
	});
});

describe("the way back from a cast", () => {
	it("ends the cast under its own bearer, and the person's own session is untouched", async () => {
		const cast = await castFrom([OWN, OWN2]);
		await act(async () => {
			await handles.exit();
		});
		expect(calls.slice(1)).toEqual([`refresh ${OWN2}`, `end ${cast}`]);
		expect(getToken()).toBe(OWN2);
		expect(sessionStorage.getItem("codefly_narrowed_session")).toBeNull();
	});

	it("a cast the host already ended is left all the same", async () => {
		const cast = await castFrom([OWN, OWN2]);
		endUnauthenticatedFor.add(cast);
		await act(async () => {
			await handles.exit();
		});
		expect(getToken()).toBe(OWN2);
	});

	it("is refused, and the person stays in the cast, when the host could not end it", async () => {
		const cast = await castFrom([OWN, OWN2, OWN3]);
		endUnavailableFor.add(cast);
		let refusal = "";
		await act(async () => {
			await handles.exit().catch((error: Error) => {
				refusal = error.message;
			});
		});
		expect(refusal).toMatch(/could not be ended/);
		expect(getToken()).toBe(cast);
		expect(JSON.parse(sessionStorage.getItem("codefly_narrowed_session") ?? "{}").bearer).toBe(cast);
		// Asked again once the host answers, the way back is taken.
		endUnavailableFor.delete(cast);
		await act(async () => {
			await handles.exit();
		});
		expect(getToken()).toBe(OWN3);
	});
});
