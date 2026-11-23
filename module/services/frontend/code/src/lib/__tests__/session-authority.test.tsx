import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import {
	AuthProvider,
	restoresSessionAtBoot,
	sessionAuthority,
	useAuth,
} from "../auth";
import { setRefreshHandler, setToken } from "../connect/token-store";

const jwt = (claims: Record<string, unknown>) =>
	`h.${btoa(JSON.stringify(claims))}.s`;
const ADMIN = jwt({ sub: "user-1", org: "org-1", or: "admin", pr: "support" });
const CURRENT = "http://localhost:3000/host.session.v1.SessionService/Current";
const END = "http://localhost:3000/host.session.v1.SessionService/End";

const seen: string[] = [];
const handles: { logout: () => Promise<{ complete: boolean }> } = {
	logout: async () => ({ complete: false }),
};
function Probe() {
	const {
		isLoading,
		isAuthenticated,
		authority,
		sessionRead,
		orgRole,
		logout,
	} = useAuth();
	useEffect(() => {
		handles.logout = logout;
	});
	if (!isLoading && isAuthenticated && !seen.includes(authority))
		seen.push(authority);
	return (
		<span data-testid="session">
			{isLoading
				? "loading"
				: `${isAuthenticated ? "in" : "out"} ${sessionRead} org:${orgRole ?? "none"}`}
		</span>
	);
}
const session = () => screen.getByTestId("session").textContent;
const mount = () =>
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
	seen.length = 0;
	window.history.replaceState({}, "", "/");
});

describe("the authority a session acts with", () => {
	// The context hides a session's roles until the host confirms it. The
	// authority is what the boundary above the shell is keyed by: were the
	// confirmation a change of it, every page load would build the shell, and
	// make its reads, twice.
	it("is the same before and after the host confirms the session", async () => {
		let confirm: () => void = () => {};
		const held = new Promise<void>((resolve) => {
			confirm = resolve;
		});
		server.use(
			http.post("/v1/auth/refresh", () =>
				HttpResponse.json({ accessToken: ADMIN, refreshToken: "r" }),
			),
			http.post(CURRENT, async () => {
				await held;
				return HttpResponse.json({});
			}),
		);
		mount();
		await waitFor(() => expect(session()).toBe("in reading org:none"));
		confirm();
		await waitFor(() => expect(session()).toBe("in confirmed org:admin"));
		expect(seen).toHaveLength(1);
	});

	it("is another for a session the host says is cast onto a team", async () => {
		server.use(
			http.post("/v1/auth/refresh", () =>
				HttpResponse.json({ accessToken: ADMIN, refreshToken: "r" }),
			),
			http.post(CURRENT, () => HttpResponse.json({ castTeamId: "team-a" })),
		);
		mount();
		await waitFor(() => expect(session()).toBe("in confirmed org:none"));
		expect(seen).toHaveLength(2);
	});

	it("differs by who is signed in, where, holding what, and as whom", () => {
		const own = {
			isAuthenticated: true,
			user: { id: "user-1", email: "jane@example.com" },
			organizationId: "org-1",
			orgRole: "admin" as const,
			platformRole: undefined,
			impersonation: { isImpersonating: false },
		};
		const authority = sessionAuthority(own, "");
		expect(sessionAuthority({ ...own }, "")).toBe(authority);
		for (const other of [
			sessionAuthority({ ...own, isAuthenticated: false, user: null }, ""),
			sessionAuthority({ ...own, user: { ...own.user, id: "user-2" } }, ""),
			sessionAuthority({ ...own, organizationId: "org-2" }, ""),
			sessionAuthority({ ...own, orgRole: "member" }, ""),
			sessionAuthority({ ...own, platformRole: "support" }, ""),
			sessionAuthority(
				{
					...own,
					impersonation: {
						isImpersonating: true,
						impersonatorId: "admin-1",
						subjectId: "user-1",
					},
				},
				"",
			),
			sessionAuthority(own, "a-narrowed-bearer"),
		]) {
			expect(other).not.toBe(authority);
		}
	});
});

describe("the sign-out page's boot", () => {
	it("restores a session everywhere but on the sign-out page", () => {
		expect(restoresSessionAtBoot("/auth/logout")).toBe(false);
		for (const path of ["/", "/auth/login", "/auth/callback", "/settings"])
			expect(restoresSessionAtBoot(path)).toBe(true);
	});

	// Opened by address with a session's cookie: nothing is restored, so the
	// tab never reads as signed in and starts none of the shell's reads; the
	// sign-out presents the cookie alone, once, and the server ends the session
	// it names.
	it("signs out with the cookie alone, restoring and reading nothing first", async () => {
		const asked: string[] = [];
		let presented: Headers | undefined;
		let body = "unread";
		server.use(
			http.post("/v1/auth/refresh", () => {
				asked.push("refresh");
				return HttpResponse.json({ accessToken: ADMIN, refreshToken: "r" });
			}),
			http.post(CURRENT, () => {
				asked.push("current");
				return HttpResponse.json({});
			}),
			http.post(END, () => {
				asked.push("end");
				return HttpResponse.json({});
			}),
			http.post("/v1/auth/logout", async ({ request }) => {
				asked.push("logout");
				presented = request.headers;
				body = await request.text();
				return HttpResponse.json({});
			}),
		);
		window.history.replaceState({}, "", "/auth/logout");
		mount();
		await waitFor(() => expect(session()).toBe("out confirmed org:none"));

		expect(await handles.logout()).toEqual({ complete: true });

		expect(asked).toEqual(["logout"]);
		expect(presented?.get("authorization")).toBeNull();
		expect(presented?.get("x-codefly-cookie-session")).toBe("optional");
		expect(body).toBe("");
		expect(seen).toEqual([]);
	});

	// Nobody is signed in on this browser: the server answers the optional
	// sign-out 204, and that is a finished sign-out, not an incomplete one.
	it("reads a sign-out with nothing to end as complete", async () => {
		server.use(
			http.post(
				"/v1/auth/logout",
				() => new HttpResponse(null, { status: 204 }),
			),
		);
		window.history.replaceState({}, "", "/auth/logout");
		mount();
		await waitFor(() => expect(session()).toBe("out confirmed org:none"));

		expect(await handles.logout()).toEqual({ complete: true });
	});

	it("keeps a refused sign-out incomplete", async () => {
		server.use(
			http.post(
				"/v1/auth/logout",
				() => new HttpResponse(null, { status: 503 }),
			),
		);
		window.history.replaceState({}, "", "/auth/logout");
		mount();
		await waitFor(() => expect(session()).toBe("out confirmed org:none"));

		expect(await handles.logout()).toEqual({ complete: false });
	});
});
