import { cleanup, screen, waitFor } from "@testing-library/react";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AdminRouteShell } from "../admin-route-shell";

const state = vi.hoisted(() => ({
	pathname: "/admin/teams/team-example",
	isAuthenticated: true,
	orgRole: "member" as string | undefined,
	replace: vi.fn(),
}));
vi.mock("next/navigation", () => ({
	usePathname: () => state.pathname,
	useRouter: () => ({ replace: state.replace }),
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		isAuthenticated: state.isAuthenticated,
		orgRole: state.orgRole,
		isLoading: false,
	}),
}));
vi.mock("@/components/admin-layout", () => ({
	AdminLayout: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/impersonation-banner", () => ({
	ImpersonationBanner: () => null,
}));
afterEach(cleanup);
beforeEach(() => {
	state.pathname = "/admin/teams/team-example";
	state.isAuthenticated = true;
	state.orgRole = "member";
	state.replace.mockClear();
});

it("admits a tenant member to team details", () => {
	render(<AdminRouteShell>Team roster</AdminRouteShell>);
	expect(screen.getByText("Team roster")).toBeTruthy();
	expect(state.replace).not.toHaveBeenCalled();
});
it.each(["/admin/users", "/admin/teams-other", "/admin/billing"])(
	"does not broaden member access to %s",
	async (path) => {
		state.pathname = path;
		render(<AdminRouteShell>Restricted</AdminRouteShell>);
		expect(screen.queryByText("Restricted")).toBeNull();
		await waitFor(() => expect(state.replace).toHaveBeenCalledWith("/"));
	},
);
it("still requires authentication", async () => {
	state.isAuthenticated = false;
	render(<AdminRouteShell>Team roster</AdminRouteShell>);
	expect(screen.queryByText("Team roster")).toBeNull();
	await waitFor(() =>
		expect(state.replace).toHaveBeenCalledWith("/auth/login"),
	);
});
