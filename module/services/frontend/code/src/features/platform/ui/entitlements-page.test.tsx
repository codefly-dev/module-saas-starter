import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { EntitlementsPage } from "./entitlements-page";

const auth = vi.hoisted(() => ({
	organizationId: "org-1",
	platformRole: "super_admin" as string | undefined,
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => auth }));
afterEach(cleanup);
beforeEach(() => {
	auth.platformRole = "super_admin";
	server.use(
		http.post(rpc("PlatformAdminService", "GetOrgEntitlements"), () =>
			HttpResponse.json({
				planName: "Example",
				entitlements: [{ feature: "seats", limit: "10", used: "2" }],
			}),
		),
	);
});
it("shows plan descriptions and hides changes from organization-only administrators", async () => {
	auth.platformRole = undefined;
	renderInApp(<EntitlementsPage />);
	expect(
		await screen.findByText("Organization members plus pending invitations."),
	).toBeTruthy();
	expect(screen.queryByRole("button", { name: "Change limit" })).toBeNull();
	expect(screen.queryByText("seats")).toBeNull();
});
it("validates the new limit beside its field", async () => {
	const override = vi.fn(() => HttpResponse.json({}));
	server.use(
		http.post(rpc("PlatformAdminService", "OverrideEntitlement"), override),
	);
	renderInApp(<EntitlementsPage />);
	fireEvent.click(await screen.findByRole("button", { name: "Change limit" }));
	fireEvent.change(screen.getByLabelText("New limit"), {
		target: { value: "1.2" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Save limit" }));
	expect(screen.getByLabelText("New limit").getAttribute("aria-invalid")).toBe(
		"true",
	);
	expect(screen.getByText(/Enter a whole number/)).toBeTruthy();
	expect(override).not.toHaveBeenCalled();
});
it("locks pending edits and retains a refused change with safe guidance", async () => {
	let finish!: () => void;
	const pending = new Promise<void>((resolve) => {
		finish = resolve;
	});
	const override = vi.fn(async () => {
		await pending;
		return HttpResponse.json(
			{ code: "permission_denied", message: "private-server-detail" },
			{ status: 403 },
		);
	});
	server.use(
		http.post(rpc("PlatformAdminService", "OverrideEntitlement"), override),
	);
	renderInApp(<EntitlementsPage />);
	fireEvent.click(await screen.findByRole("button", { name: "Change limit" }));
	fireEvent.click(screen.getByRole("button", { name: "Save limit" }));
	await waitFor(() => expect(override).toHaveBeenCalledTimes(1));
	expect(
		(screen.getByLabelText("New limit") as HTMLInputElement).disabled,
	).toBe(true);
	expect(
		(screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
			.disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(screen.getByRole("dialog")).toBeTruthy();
	finish();
	expect(await screen.findByText("Limit wasn’t updated")).toBeTruthy();
	expect(screen.queryByText("private-server-detail")).toBeNull();
});
