import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { AuthProvider, useAuth } from "../auth";
import { setRefreshHandler, setToken } from "../connect/token-store";

// A cast token still carries the person's organization role. The chrome reads
// roles from useAuth, so a cast must read as holding none of them: what the
// host says the session is (Session.cast_team_id), never what the token says
// the person is. Nothing is computed here; the host decides every call.

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims))}.s`;
const ADMIN = jwt({ sub: "user-1", org: "org-1", or: "admin", pr: "super_admin" });

function Probe() {
	const { isLoading, orgRole, platformRole, castTeamId } = useAuth();
	if (isLoading) return <span data-testid="roles">loading</span>;
	return (
		<span data-testid="roles">
			{`org:${orgRole ?? "none"} platform:${platformRole ?? "none"} cast:${castTeamId || "none"}`}
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

function boot(session: Record<string, unknown>) {
	server.use(
		http.post("/v1/auth/refresh", () => HttpResponse.json({ accessToken: ADMIN, refreshToken: "r" })),
		http.post("http://localhost:3000/host.session.v1.SessionService/Current", () => HttpResponse.json(session)),
	);
	render(
		<AuthProvider>
			<Probe />
		</AuthProvider>,
	);
}

describe("roles the chrome reads", () => {
	it("a cast session holds none of the person's roles", async () => {
		boot({ castTeamId: "team-stewards" });
		await waitFor(() =>
			expect(screen.getByTestId("roles").textContent).toBe("org:none platform:none cast:team-stewards"),
		);
	});

	it("an ordinary session keeps the roles its token carries", async () => {
		boot({});
		await waitFor(() => expect(screen.getByTestId("roles").textContent).toMatch(/^org:admin/));
		expect(screen.getByTestId("roles").textContent).toBe("org:admin platform:super_admin cast:none");
	});
});
