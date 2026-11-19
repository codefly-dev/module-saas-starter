import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { SSOAdminPage } from "./sso-admin-page";

// SSOAdminPage reads the active org from useAuth (throws outside an
// AuthProvider). Stub it to a fixed org so the status query fires.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		switchOrganization: async () => {},
	}),
}));

afterEach(cleanup);

describe("SSOAdminPage admin container", () => {
	it("renders the active SSO connection the service returns", async () => {
		server.use(
			http.post(rpc("SSOAdminService", "GetSSO"), () =>
				HttpResponse.json({
					provider: "okta",
					status: "active",
					connectionId: "conn_test123",
				}),
			),
		);
		renderInApp(<SSOAdminPage />);
		expect(
			await screen.findByText("SSO is live for this organization"),
		).toBeTruthy();
	});
});

it("requires confirmation before disabling single sign-on and supports cancellation", async () => {
  const disable = vi.fn(() => HttpResponse.json({}));
  server.use(
    http.post(rpc("SSOAdminService", "GetSSO"), () => HttpResponse.json({ status: "active", provider: "okta" })),
    http.post(rpc("SSOAdminService", "Disable"), disable),
  );
  renderInApp(<SSOAdminPage />);
  fireEvent.click(await screen.findByRole("button", { name: "Disable SSO" }));
  const dialog = await screen.findByRole("alertdialog");
  expect(disable).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
  expect(disable).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Disable SSO" }));
  fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Disable single sign-on" }));
  await waitFor(() => expect(disable).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
});
