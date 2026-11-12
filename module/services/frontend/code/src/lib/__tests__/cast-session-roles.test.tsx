import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, useAuth } from "../auth";
import { setRefreshHandler, setToken } from "../connect/token-store";

// A cast token still carries the person's organization role. The chrome reads
// roles from useAuth, so a session the host has not confirmed, and a cast,
// must read as holding none of them: what the host says the session is
// (Session.cast_team_id), never what the token says the person is. An
// unconfirmed session (in flight, failed, or freshly swapped) is never
// resolved to the token's roles. Nothing is computed here; the host decides.

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims))}.s`;
const claims = { sub: "user-1", org: "org-1", or: "admin", pr: "super_admin" };
const ADMIN = jwt(claims);
const CAST = jwt({ ...claims, n: "cast" });
const CURRENT = "http://localhost:3000/host.session.v1.SessionService/Current";

const handles: { enter: (bearer: string, as: string) => void } = { enter: () => {} };
function Probe() {
	const { isLoading, orgRole, platformRole, castTeamId, sessionRead, enterRoleView } = useAuth();
	useEffect(() => {
		handles.enter = enterRoleView;
	});
	if (isLoading) return <span data-testid="roles">loading</span>;
	return (
		<span data-testid="roles">
			{`${sessionRead} org:${orgRole ?? "none"} platform:${platformRole ?? "none"} cast:${castTeamId || "none"}`}
		</span>
	);
}
const roles = () => screen.getByTestId("roles").textContent;

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

function boot(current: Parameters<typeof http.post>[1]) {
	server.use(
		http.post("/v1/auth/refresh", () => HttpResponse.json({ accessToken: ADMIN, refreshToken: "r" })),
		http.post(CURRENT, current),
	);
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
}

describe("roles the chrome reads", () => {
	it("a confirmed cast holds none of the person's roles", async () => {
		boot(() => HttpResponse.json({ castTeamId: "team-stewards" }));
		await waitFor(() => expect(roles()).toBe("confirmed org:none platform:none cast:team-stewards"));
	});

	it("a confirmed ordinary session keeps the roles its token carries", async () => {
		boot(() => HttpResponse.json({}));
		await waitFor(() => expect(roles()).toBe("confirmed org:admin platform:super_admin cast:none"));
	});

	it("a failed read is unconfirmed and holds none of the token's roles", async () => {
		boot(() => HttpResponse.json({ code: "unavailable", message: "down" }, { status: 503 }));
		await waitFor(() => expect(roles()).toBe("failed org:none platform:none cast:none"));
	});

	it("a read still in flight is unconfirmed, and confirms when it lands", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((r) => (release = r));
		boot(async () => {
			await gate;
			return HttpResponse.json({});
		});
		await waitFor(() => expect(roles()).toBe("reading org:none platform:none cast:none"));
		release();
		await waitFor(() => expect(roles()).toBe("confirmed org:admin platform:super_admin cast:none"));
	});

	it("a token swapped into a cast is unconfirmed until the cast's own answer lands, never the previous token's", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((r) => (release = r));
		boot(async ({ request }) => {
			if (request.headers.get("authorization") === `Bearer ${CAST}`) {
				await gate;
				return HttpResponse.json({ castTeamId: "team-stewards" });
			}
			return HttpResponse.json({});
		});
		await waitFor(() => expect(roles()).toBe("confirmed org:admin platform:super_admin cast:none"));
		act(() => handles.enter(CAST, "team Stewards"));
		await waitFor(() => expect(roles()).toMatch(/^reading org:none platform:none cast:none/));
		release();
		await waitFor(() => expect(roles()).toBe("confirmed org:none platform:none cast:team-stewards"));
	});
});
