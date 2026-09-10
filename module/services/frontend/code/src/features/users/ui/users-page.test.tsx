import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { UsersPage } from "./users-page";

const { enterImpersonation } = vi.hoisted(() => ({
	enterImpersonation: vi.fn(),
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => ({ enterImpersonation }) }));

const IMPERSONATION_TOKEN = "header.payload.signature";

function searchReturnsOneUser() {
	server.use(
		http.post(rpc("PlatformAdminService", "SearchUsers"), () =>
			HttpResponse.json({
				users: [
					{
						uuid: "user-1",
						primaryEmail: "admin@acme.test",
						status: 1,
						emailVerified: true,
						profile: {},
					},
				],
			}),
		),
	);
}

// The row action menu opens on pointerdown rather than click, and its trigger
// is the row's only icon button.
function openRowActions() {
	const buttons = screen.getAllByRole("button");
	const trigger = buttons[buttons.length - 1];
	fireEvent.pointerDown(trigger);
	fireEvent.click(trigger);
}

afterEach(() => {
	cleanup();
	enterImpersonation.mockClear();
});

describe("UsersPage admin container", () => {
	it("renders the users the platform admin service returns", async () => {
		searchReturnsOneUser();
		renderInApp(<UsersPage />);
		// Email shows in the Email column and again as the Name fallback (empty
		// profile), so match all occurrences rather than a single node.
		expect(
			(await screen.findAllByText("admin@acme.test")).length,
		).toBeGreaterThan(0);
	});

	// Impersonating enters the session directly. The token is a live credential,
	// so it must reach the auth provider and never the document.
	it("installs the minted token as the session instead of displaying it", async () => {
		searchReturnsOneUser();
		server.use(
			http.post(rpc("PlatformAdminService", "ImpersonateUser"), () =>
				HttpResponse.json({ accessToken: IMPERSONATION_TOKEN }),
			),
		);
		renderInApp(<UsersPage />);
		await screen.findAllByText("admin@acme.test");

		openRowActions();
		fireEvent.click(await screen.findByText("Impersonate"));

		await waitFor(() =>
			expect(enterImpersonation).toHaveBeenCalledWith(IMPERSONATION_TOKEN),
		);
		expect(screen.queryByText(IMPERSONATION_TOKEN)).toBeNull();
	});
});
