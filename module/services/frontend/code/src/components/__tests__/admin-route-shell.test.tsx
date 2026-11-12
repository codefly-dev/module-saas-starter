import { cleanup, render, screen, waitFor } from "@testing-library/react";
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
vi.mock("@/components/empty-state", () => ({
	EmptyState: ({
		title,
		description,
	}: {
		title: string;
		description: string;
	}) => (
		<div>
			<h1>{title}</h1>
			<p>{description}</p>
		</div>
	),
}));
vi.mock("@/components/admin-layout", () => ({
	AdminLayout: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/impersonation-banner", () => ({
	ImpersonationBanner: () => null,
}));
vi.mock("@/features/role-view/role-view", () => ({
	RoleViewBanner: () => <div>narrowed session bar</div>,
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
	// A view or a cast is said on admin pages too, admitted or refused.
	expect(screen.getByText("narrowed session bar")).toBeTruthy();
	expect(state.replace).not.toHaveBeenCalled();
});
it.each(["/admin/users", "/admin/teams-other", "/admin/billing"])(
	"does not broaden member access to %s",
	async (path) => {
		state.pathname = path;
		render(<AdminRouteShell>Restricted</AdminRouteShell>);
		expect(screen.queryByText("Restricted")).toBeNull();
		expect(screen.getByText("You don't have access to Admin")).toBeTruthy();
		expect(
			screen.getByText(/Your role in this organization is member/),
		).toBeTruthy();
		expect(state.replace).not.toHaveBeenCalled();
	},
);
it("names a role that is absent", () => {
	state.pathname = "/admin/users";
	state.orgRole = undefined;
	render(<AdminRouteShell>Restricted</AdminRouteShell>);
	expect(
		screen.getByText(/Your role in this organization is none/),
	).toBeTruthy();
});
it("still requires authentication", async () => {
	state.isAuthenticated = false;
	render(<AdminRouteShell>Team roster</AdminRouteShell>);
	expect(screen.queryByText("Team roster")).toBeNull();
	await waitFor(() =>
		expect(state.replace).toHaveBeenCalledWith("/auth/login"),
	);
});
