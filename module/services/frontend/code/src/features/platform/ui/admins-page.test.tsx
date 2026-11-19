import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { AdminsPage } from "./admins-page";

const auth = vi.hoisted(() => ({
	platformRole: "super_admin" as string | undefined,
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));
afterEach(cleanup);
beforeEach(() => {
	auth.platformRole = "super_admin";
	server.use(
		http.post(rpc("PlatformAdminService", "ListPlatformAdmins"), () =>
			HttpResponse.json({
				admins: [{ userId: "person-1", platformRole: "support" }],
			}),
		),
		http.post(rpc("UserService", "GetUser"), () =>
			HttpResponse.json({
				uuid: "person-1",
				primaryEmail: "jane@example.com",
				profile: { name: "Jane Doe" },
			}),
		),
	);
});

it("requires an explicit role choice in a labelled kit field", async () => {
	renderInApp(<AdminsPage />);
	fireEvent.click(screen.getByRole("button", { name: "Grant role" }));
	const dialog = screen.getByRole("dialog");
	expect(
		within(dialog).getByRole("combobox", { name: "Role" }).textContent,
	).toContain("Choose a role");
	expect(
		(within(dialog).getByRole("button", { name: "Grant" }) as HTMLButtonElement)
			.disabled,
	).toBe(true);
});

it("hides changes from people without platform administration access", async () => {
	auth.platformRole = "support";
	renderInApp(<AdminsPage />);
	await screen.findByText("Jane Doe (jane@example.com)");
	expect(screen.queryByRole("button", { name: "Grant role" })).toBeNull();
	expect(screen.queryByRole("button", { name: /Remove access/ })).toBeNull();
});

it("names the person and retains the confirmation when removal fails", async () => {
	server.use(
		http.post(rpc("PlatformAdminService", "RevokePlatformRole"), () =>
			HttpResponse.json(
				{ code: "permission_denied", message: "private-server-detail" },
				{ status: 403 },
			),
		),
	);
	renderInApp(<AdminsPage />);
	fireEvent.click(
		await screen.findByRole("button", {
			name: "Remove access for Jane Doe (jane@example.com)",
		}),
	);
	const dialog = screen.getByRole("alertdialog");
	expect(within(dialog).getByText("Jane Doe (jane@example.com)")).toBeTruthy();
	fireEvent.click(
		within(dialog).getByRole("button", { name: "Remove access" }),
	);
	expect(
		await within(dialog).findByText("Couldn't remove access"),
	).toBeTruthy();
	expect(screen.queryByText("private-server-detail")).toBeNull();
	expect(screen.getByRole("alertdialog")).toBeTruthy();
});

it("keeps a pending removal open and submits it only once", async () => {
	let finish!: () => void;
	const pending = new Promise<void>((resolve) => {
		finish = resolve;
	});
	const remove = vi.fn(async () => {
		await pending;
		return HttpResponse.json({});
	});
	server.use(
		http.post(rpc("PlatformAdminService", "RevokePlatformRole"), remove),
	);
	renderInApp(<AdminsPage />);
	fireEvent.click(
		await screen.findByRole("button", { name: /Remove access for Jane/ }),
	);
	const dialog = screen.getByRole("alertdialog");
	const button = within(dialog).getByRole("button", { name: "Remove access" });
	fireEvent.click(button);
	fireEvent.click(button);
	await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
	expect(
		(
			within(dialog).getByRole("button", {
				name: "Cancel",
			}) as HTMLButtonElement
		).disabled,
	).toBe(true);
	fireEvent.keyDown(dialog, { key: "Escape" });
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	finish();
	await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
});
