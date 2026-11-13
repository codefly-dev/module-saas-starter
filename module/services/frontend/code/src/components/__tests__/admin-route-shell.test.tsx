import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminRouteShell } from "../admin-route-shell";

const state = vi.hoisted(() => ({
	pathname: "/admin/teams/team-example",
	isAuthenticated: true,
	orgRole: "member" as string | undefined,
	castTeamId: "",
	sessionRead: "confirmed" as string,
	replace: vi.fn(),
	heldPlugins: new Set<string>(),
	pagesLoaded: true,
}));
vi.mock("@/lib/providers", () => ({
	useFrontendConfig: () => ({
		routes: [
			{
				id: "models",
				path: "/admin/models",
				plugin: "model",
				requiredRole: "admin",
			},
		],
	}),
}));
vi.mock("@/lib/plugins/held-pages", () => ({
	useHeldPlugins: () => state.heldPlugins,
}));
vi.mock("@/solutions/SolutionsMenu", () => ({
	useRegisteredSolutionsLoaded: () => state.pagesLoaded,
}));
vi.mock("next/navigation", () => ({
	usePathname: () => state.pathname,
	useRouter: () => ({ replace: state.replace }),
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		isAuthenticated: state.isAuthenticated,
		orgRole: state.orgRole,
		castTeamId: state.castTeamId,
		sessionRead: state.sessionRead,
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
	state.castTeamId = "";
	state.sessionRead = "confirmed";
	state.replace.mockClear();
	state.heldPlugins = new Set();
	state.pagesLoaded = true;
});

// A page the host bundles (model's /admin/models) is its role holders' to
// open, administrators or not; a member holding no role on it is refused as
// on any admin page. Red before: the shell admitted administrators alone.
describe("a page the host bundles", () => {
	it("admits a member holding a role on it", () => {
		state.pathname = "/admin/models";
		state.heldPlugins = new Set(["model"]);
		render(<AdminRouteShell>Installed models</AdminRouteShell>);
		expect(screen.getByText("Installed models")).toBeTruthy();
	});
	it("refuses a member holding none", () => {
		state.pathname = "/admin/models";
		render(<AdminRouteShell>Installed models</AdminRouteShell>);
		expect(screen.queryByText("Installed models")).toBeNull();
		expect(screen.getByText("You don't have access to Admin")).toBeTruthy();
	});
	it("refuses nothing before the member's pages are read", () => {
		state.pathname = "/admin/models";
		state.pagesLoaded = false;
		render(<AdminRouteShell>Installed models</AdminRouteShell>);
		expect(screen.queryByText("Installed models")).toBeNull();
		expect(screen.queryByText("You don't have access to Admin")).toBeNull();
	});
	it("opens no other admin page to them", () => {
		state.pathname = "/admin/users";
		state.heldPlugins = new Set(["model"]);
		render(<AdminRouteShell>Users</AdminRouteShell>);
		expect(screen.queryByText("Users")).toBeNull();
	});
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

// "No role" is not "unconfirmed": while the host is being asked the shell
// waits, and when it could not be asked it says so. Neither is a denial, and
// neither says the person's role is none.
it("shows no denial while the session is being confirmed", () => {
	state.orgRole = undefined;
	state.sessionRead = "reading";
	render(<AdminRouteShell>Team roster</AdminRouteShell>);
	expect(screen.queryByText(/You don't have access/)).toBeNull();
	expect(screen.queryByText(/is none/)).toBeNull();
	expect(screen.getByText("narrowed session bar")).toBeTruthy();
});

it("says it could not confirm the session, not that the role is none", () => {
	state.orgRole = undefined;
	state.sessionRead = "failed";
	render(<AdminRouteShell>Team roster</AdminRouteShell>);
	expect(
		screen.getByText("Could not confirm what this session is"),
	).toBeTruthy();
	expect(screen.queryByText(/is none/)).toBeNull();
});

// A cast holds no organization role, so useAuth reads orgRole as none. It is
// still a member of the team it acts as: the team pages admit it, and every
// other admin page refuses it by naming the cast, not the person's role.
describe("a cast session", () => {
	beforeEach(() => {
		state.orgRole = undefined;
		state.castTeamId = "team-example";
	});

	it("is admitted to team details", () => {
		render(<AdminRouteShell>Team roster</AdminRouteShell>);
		expect(screen.getByText("Team roster")).toBeTruthy();
		expect(screen.queryByText(/You don't have access/)).toBeNull();
	});

	it.each(["/admin/users", "/admin/billing", "/admin/organizations"])(
		"is refused %s by naming the cast, never a role of its own",
		(path) => {
			state.pathname = path;
			render(<AdminRouteShell>Restricted</AdminRouteShell>);
			expect(screen.queryByText("Restricted")).toBeNull();
			expect(screen.getByText("You don't have access to Admin")).toBeTruthy();
			expect(screen.getByText(/This session acts as a team/)).toBeTruthy();
			expect(screen.queryByText(/Your role in this organization/)).toBeNull();
		},
	);
});
