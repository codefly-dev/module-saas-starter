import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { Profiler, type ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

const authState = vi.hoisted(() => ({
	platformRole: "super_admin" as string | undefined,
	organizationId: undefined as string | undefined,
	isAuthenticated: true,
	user: { id: "admin-1" },
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

import { AdminsPage } from "./admins-page";
import { EntitlementsPage } from "./entitlements-page";
import { SessionsPage } from "./sessions-page";

afterEach(cleanup);

const settle = () => new Promise((resolve) => setTimeout(resolve, 300));

const refused = () =>
	HttpResponse.json(
		{ code: "unavailable", message: "unavailable" },
		{ status: 503 },
	);

// Each page feeds `useReactTable` the rows of a read. TanStack Table answers a
// new `data` array with a queued page reset that sets state on the page. A page
// that hands it a fresh `[]` while the read has no answer — refused, or never
// sent because it is disabled — is quiet until anything re-renders it, and then
// re-renders forever. In a browser that froze the tab; here it shows as commits
// that never stop.
describe.each<{ name: string; method?: string; page: () => ReactElement }>([
	{
		name: "AdminsPage",
		method: "ListPlatformAdmins",
		page: () => <AdminsPage />,
	},
	// The table is replaced by the refusal here, so this one never looped; it
	// holds the same rule so a change to that layout cannot start one.
	{
		name: "SessionsPage",
		method: "ListActiveSessions",
		page: () => <SessionsPage />,
	},
	// No organization selected: the entitlements read is disabled.
	{ name: "EntitlementsPage", page: () => <EntitlementsPage /> },
])("$name", ({ method, page }) => {
	it("settles when re-rendered with no answer to show", async () => {
		if (method) {
			server.use(http.post(rpc("PlatformAdminService", method), refused));
		}
		let commits = 0;
		const tree = () => (
			<Profiler id="page" onRender={() => commits++}>
				{page()}
			</Profiler>
		);
		const { rerender } = renderInApp(tree());
		expect(await screen.findByRole("heading", { level: 1 })).toBeTruthy();
		await settle();

		rerender(tree());
		await settle();
		const settled = commits;
		await settle();
		expect(commits - settled).toBe(0);
	});
});

it("explains denied session access without offering retry or an empty list", async () => {
	server.use(
		http.post(rpc("PlatformAdminService", "ListActiveSessions"), () =>
			HttpResponse.json(
				{ code: "permission_denied", message: "raw-internal-message" },
				{ status: 403 },
			),
		),
	);
	renderInApp(<SessionsPage />);
	expect(
		await screen.findByText(/Ask a platform administrator to grant access/),
	).toBeTruthy();
	expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	expect(screen.queryByText(/No one is signed in/)).toBeNull();
	expect(screen.queryByText("raw-internal-message")).toBeNull();
});

it("keeps a refused session revocation open and never exposes server details", async () => {
	let finish!: () => void;
	const held = new Promise<void>((resolve) => {
		finish = resolve;
	});
	let calls = 0;
	server.use(
		http.post(rpc("PlatformAdminService", "RevokeSession"), async () => {
			calls++;
			await held;
			return HttpResponse.json(
				{ code: "permission_denied", message: "private diagnostics" },
				{ status: 403 },
			);
		}),
	);
	renderInApp(<SessionsPage />);
	fireEvent.click(
		await screen.findByRole("button", { name: "Session actions" }),
	);
	fireEvent.click(screen.getByRole("menuitem", { name: "Force logout" }));
	let dialog = screen.getByRole("alertdialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
	expect(calls).toBe(0);
	fireEvent.click(screen.getByRole("button", { name: "Session actions" }));
	fireEvent.click(screen.getByRole("menuitem", { name: "Force logout" }));
	dialog = screen.getByRole("alertdialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Force logout" }));
	await waitFor(() => expect(calls).toBe(1));
	expect(
		within(dialog)
			.getByRole("button", { name: "Cancel" })
			.hasAttribute("disabled"),
	).toBe(true);
	fireEvent.keyDown(dialog, { key: "Escape" });
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	finish();
	expect(
		await within(dialog).findByText("Couldn’t sign out this session"),
	).toBeTruthy();
	expect(screen.queryByText("private diagnostics")).toBeNull();
});
