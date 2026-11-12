import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppearanceProvider } from "@/lib/appearance-provider";
import { AuthProvider, useAuth } from "@/lib/auth";
import { setRefreshHandler, setToken } from "@/lib/connect/token-store";
import { FrontendConfigProvider } from "@/lib/providers";
import { server } from "@/test/setup";

vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
	notFound: () => {
		throw new Error("not found");
	},
}));
vi.mock("@/features/notifications/ui/notification-bell", () => ({
	NotificationBell: () => null,
}));
vi.mock("@/features/notifications/ui/notification-banner", () => ({
	NotificationBanner: () => null,
}));
vi.mock("@/components/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@/components/rate-limit-banner", () => ({
	RateLimitBanner: () => null,
}));

import { AdminLayout } from "@/components/admin-layout";
import applicationFrontendConfig from "../../../frontend.config";

// The admin menus, end to end through the real auth provider: a cast token
// still carries the person's organization role, and only the host's answer
// (Session.cast_team_id) says the session is a cast. The menu must follow
// the host, never the token.
const jwt = (claims: Record<string, unknown>) =>
	`h.${btoa(JSON.stringify(claims))}.s`;
const ORG_ADMIN = jwt({ sub: "user-1", org: "org-1", or: "admin" });
const CURRENT = "http://localhost:3000/host.session.v1.SessionService/Current";

function Wrap({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return (
		<FrontendConfigProvider config={applicationFrontendConfig}>
			<QueryClientProvider client={queryClient}>
				<AuthProvider>
					<AppearanceProvider config={applicationFrontendConfig}>
						{children}
					</AppearanceProvider>
				</AuthProvider>
			</QueryClientProvider>
		</FrontendConfigProvider>
	);
}

// What the host said this session is, so a test asserts on the menu only
// once the answer it is about has landed.
function SessionProbe() {
	const { accessToken, sessionRead, orgRole, castTeamId } = useAuth();
	return (
		<span data-testid="session">
			{`${accessToken ? "signed-in" : "signed-out"} ${sessionRead} org:${orgRole ?? "none"} cast:${castTeamId || "none"}`}
		</span>
	);
}
const session = () => screen.getByTestId("session").textContent;

function renderAs(session: Record<string, unknown>) {
	server.use(
		http.post("/v1/auth/refresh", () =>
			HttpResponse.json({ accessToken: ORG_ADMIN, refreshToken: "r" }),
		),
		http.post(CURRENT, () => HttpResponse.json(session)),
	);
	render(
		<Wrap>
			<AdminLayout>
				<SessionProbe />
			</AdminLayout>
		</Wrap>,
	);
}

const adminMenu = () => screen.queryByRole("button", { name: /^Admin$/ });

afterEach(() => {
	cleanup();
	setRefreshHandler(null);
	setToken(null);
	localStorage.clear();
	sessionStorage.clear();
});

describe("the admin menu", () => {
	it("is offered to an organization administrator who is not cast", async () => {
		renderAs({});
		await waitFor(() =>
			expect(session()).toBe("signed-in confirmed org:admin cast:none"),
		);
		expect(adminMenu()).toBeTruthy();
	});

	it("is not offered to a cast session, whatever role its token carries", async () => {
		renderAs({ castTeamId: "team-stewards" });
		await waitFor(() =>
			expect(session()).toBe("signed-in confirmed org:none cast:team-stewards"),
		);
		expect(adminMenu()).toBeNull();
	});
});
