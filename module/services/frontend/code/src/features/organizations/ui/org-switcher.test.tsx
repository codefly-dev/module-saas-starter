import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SidebarProvider } from "@/components/ui/sidebar";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

const authState = vi.hoisted(() => ({
	organizationId: "org-acme",
	switchOrganization: vi.fn(async () => {}),
	logout: vi.fn(async () => {}),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

import { OrgSwitcher } from "./org-switcher";

afterEach(() => {
	cleanup();
	authState.switchOrganization.mockClear();
});

function organizations(canCreate: boolean) {
	server.use(
		http.post(rpc("OrganizationService", "ListOrganizations"), () =>
			HttpResponse.json({
				organizations: [
					{ id: "org-acme", name: "Acme", slug: "acme" },
					{ id: "org-beta", name: "Beta", slug: "beta" },
				],
				canCreate,
			}),
		),
	);
}

function open() {
	renderInApp(
		<SidebarProvider>
			<OrgSwitcher />
		</SidebarProvider>,
	);
}

describe("OrgSwitcher", () => {
	it("shows the current organization and switches to another", async () => {
		organizations(true);
		open();
		const trigger = await screen.findByRole("button", { name: "Organization" });
		await waitFor(() => expect(trigger.textContent).toContain("Acme"));
		fireEvent.click(trigger);
		const beta = await screen.findByRole("menuitem", { name: "Beta" });
		expect(screen.getByRole("menuitem", { name: "Leave Acme" })).toBeTruthy();
		fireEvent.click(beta);
		await waitFor(() =>
			expect(authState.switchOrganization).toHaveBeenCalledWith("org-beta"),
		);
	});

	it("does not call a failed read an empty membership", async () => {
		server.use(
			http.post(rpc("OrganizationService", "ListOrganizations"), () =>
				HttpResponse.json(
					{ code: "permission_denied", message: "private policy" },
					{ status: 403 },
				),
			),
		);
		open();
		await screen.findByText("Organizations unavailable");
		fireEvent.click(screen.getByRole("button", { name: "Organization" }));
		expect(await screen.findByText("Couldn’t load organizations")).toBeTruthy();
		expect(screen.queryByText("No organization")).toBeNull();
		expect(screen.queryByText("private policy")).toBeNull();
		expect(
			screen.queryByRole("menuitem", { name: "Create organization" }),
		).toBeNull();
		expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	});

	it("does not offer creation the deployment's policy refuses", async () => {
		organizations(false);
		open();
		const trigger = await screen.findByRole("button", { name: "Organization" });
		await waitFor(() => expect(trigger.textContent).toContain("Acme"));
		fireEvent.click(trigger);
		await screen.findByRole("menuitem", { name: "Beta" });
		expect(
			screen.queryByRole("menuitem", { name: "Create organization" }),
		).toBeNull();
	});
});
