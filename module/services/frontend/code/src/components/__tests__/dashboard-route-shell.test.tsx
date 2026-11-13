import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { server } from "@/test/setup";
import { DashboardRouteShell } from "../dashboard-route-shell";

// A person holding several roles on several installations, as an
// administrator does: the shape that drew a bare per-installation role list
// above every dashboard page.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		isAuthenticated: true,
		isLoading: false,
		accessToken: "own-session",
		organizationId: "org-example",
		sessionRead: "confirmed",
		castTeamId: "",
		roleView: [],
		narrowedSessionEnd: null,
		enterRoleView: vi.fn(),
		exitRoleView: vi.fn(() => Promise.resolve()),
		dismissNarrowedSessionEnd: vi.fn(),
	}),
}));
vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}));
vi.mock("@/components/admin-layout", () => ({
	AdminLayout: ({ children }: { children: ReactNode }) => <main>{children}</main>,
}));
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }));
vi.mock("@/components/impersonation-banner", () => ({
	ImpersonationBanner: () => null,
}));
vi.mock("@/features/onboarding/ui/onboarding-gate", () => ({
	OnboardingGate: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
// The one way to narrow a view: the kit's casting, which the shell mounts.
vi.mock("@/features/role-view/team-casting", () => ({
	HostCasting: ({ children }: { children: ReactNode }) => (
		<div data-testid="casting">{children}</div>
	),
}));
afterEach(cleanup);

describe("the dashboard shell", () => {
	it("offers no second way to narrow the view beside casting", async () => {
		server.use(
			http.post(
				"http://localhost:3000/host.installation.v1.InstallationService/ListHeld",
				() =>
					HttpResponse.json({
						installations: [
							{
								installation: { id: "inst-a", target: "solution-a" },
								roles: ["editor", "installer", "viewer"],
							},
							{
								installation: { id: "inst-b", target: "module-b" },
								roles: ["steward", "viewer"],
							},
						],
					}),
			),
		);
		renderInApp(<DashboardRouteShell>Dashboard page</DashboardRouteShell>);
		const casting = screen.getByTestId("casting");
		expect(casting.textContent).toBe("Dashboard page");
		// Red before: the shell mounted the old per-installation switcher,
		// which drew "all of mine", "editor", "steward"… as bare buttons above
		// the page, outside any menu.
		await expect(
			screen.findAllByRole("button", { name: "all of mine" }, { timeout: 500 }),
		).rejects.toThrow();
		expect(screen.queryByLabelText("View as")).toBeNull();
		expect(casting.textContent).toBe("Dashboard page");
	});
});
