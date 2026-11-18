import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// The page reads the active tenant from the signed session. Pin it so the
// container renders the table (not the "select an organization" empty state).
const auth = vi.hoisted(() => ({
	organizationId: "org-1",
	orgRole: "admin" as "admin" | "member" | undefined,
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: auth.organizationId,
		orgRole: auth.orgRole,
		switchOrganization: async () => {},
	}),
}));

import { APIKeysPage } from "./api-keys-page";

afterEach(cleanup);
beforeEach(() => {
	auth.organizationId = "org-1";
	auth.orgRole = "admin";
});

describe("APIKeysPage admin container", () => {
	it("lists the keys past the first page by following the page token", async () => {
		const asked: unknown[] = [];
		server.use(
			http.post(rpc("APIKeyService", "ListAPIKeys"), async ({ request }) => {
				const body = (await request.json()) as { pageToken?: string };
				asked.push(body.pageToken ?? "");
				return HttpResponse.json(
					body.pageToken === "after-newest"
						? {
								keys: [
									{ id: "key-2", organizationId: "org-1", name: "Oldest key" },
								],
							}
						: {
								keys: [
									{ id: "key-1", organizationId: "org-1", name: "Newest key" },
								],
								nextPageToken: "after-newest",
							},
				);
			}),
		);
		renderInApp(<APIKeysPage />);
		await screen.findByRole("button", { name: "Revoke Oldest key" });
		expect(
			screen.getByRole("button", { name: "Revoke Newest key" }),
		).toBeTruthy();
		expect(asked).toEqual(["", "after-newest"]);
	});
	it("revokes the key in its organization and refreshes the visible list", async () => {
		let revoked = false;
		let requestBody: unknown;
		server.use(
			http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
				HttpResponse.json({
					keys: revoked
						? []
						: [{ id: "key-1", organizationId: "org-1", name: "Example key" }],
				}),
			),
			http.post(rpc("APIKeyService", "RevokeAPIKey"), async ({ request }) => {
				requestBody = await request.json();
				revoked = true;
				return HttpResponse.json({});
			}),
		);
		renderInApp(<APIKeysPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Revoke Example key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));
		await screen.findByRole("heading", { name: "No API keys yet", level: 2 });
		expect(screen.queryByRole("table", { name: "API keys" })).toBeNull();
		expect(requestBody).toEqual({ id: "key-1", organizationId: "org-1" });
		expect(
			screen.queryByRole("button", { name: "Revoke Example key" }),
		).toBeNull();
	});
	it("keeps the named confirmation open while revoking and explains a denial inline", async () => {
		let finish!: () => void;
		const pending = new Promise<void>((resolve) => {
			finish = resolve;
		});
		const revoke = vi.fn(async () => {
			await pending;
			return HttpResponse.json(
				{
					code: "permission_denied",
					message: "internal permission key:revoke",
				},
				{ status: 403 },
			);
		});
		server.use(
			http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
				HttpResponse.json({
					keys: [{ id: "key-1", organizationId: "org-1", name: "Example key" }],
				}),
			),
			http.post(rpc("APIKeyService", "RevokeAPIKey"), revoke),
		);
		renderInApp(<APIKeysPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Revoke Example key" }),
		);
		const dialog = await screen.findByRole("alertdialog", {
			name: "Revoke Example key?",
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));
		await waitFor(() => expect(revoke).toHaveBeenCalledOnce());
		expect(
			within(dialog)
				.getByRole("button", { name: "Revoking…" })
				.hasAttribute("disabled"),
		).toBe(true);
		expect(
			within(dialog)
				.getByRole("button", { name: "Cancel" })
				.hasAttribute("disabled"),
		).toBe(true);
		fireEvent.keyDown(dialog, { key: "Escape" });
		expect(screen.getByRole("alertdialog")).toBeTruthy();
		finish();
		expect((await within(dialog).findByRole("alert")).textContent).toContain(
			"Ask your organization administrator for access.",
		);
		expect(dialog.textContent).not.toContain("key:revoke");
		fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});
	it("opens the creation dialog and displays the newly created secret", async () => {
		const create = vi.fn(() =>
			HttpResponse.json({ plaintextKey: "sk_test_example_secret" }),
		);
		server.use(http.post(rpc("APIKeyService", "CreateAPIKey"), create));
		renderInApp(<APIKeysPage />);
		fireEvent.click(screen.getByRole("button", { name: "Create key" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByRole("textbox", { name: "Name" }), {
			target: { value: "Example integration" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "Create key" }));
		expect(await screen.findByText("sk_test_example_secret")).toBeTruthy();
		expect(create).toHaveBeenCalledOnce();
		expect(within(dialog).queryByRole("button", { name: "Close" })).toBeNull();
		fireEvent.click(
			within(dialog).getByRole("checkbox", {
				name: "I have saved this key securely",
			}),
		);
		fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
	it("explains the missing organization instead of leaving a dead create button", async () => {
		auth.organizationId = "";
		renderInApp(<APIKeysPage />);
		fireEvent.click(screen.getByRole("button", { name: "Create key" }));
		expect(
			await screen.findByText("Select an organization to create its API key."),
		).toBeTruthy();
	});
	it("shows a rejected creation inline and allows retrying the form", async () => {
		server.use(
			http.post(rpc("APIKeyService", "CreateAPIKey"), () =>
				HttpResponse.json(
					{
						code: "permission_denied",
						message: "You need organization admin access.",
					},
					{ status: 403 },
				),
			),
		);
		renderInApp(<APIKeysPage />);
		fireEvent.click(screen.getByRole("button", { name: "Create key" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByRole("textbox", { name: "Name" }), {
			target: { value: "Example integration" },
		});
		fireEvent.submit(
			within(dialog).getByRole("form", { name: "Create API key" }),
		);
		expect((await within(dialog).findByRole("alert")).textContent).toContain(
			"Ask your organization administrator for access.",
		);
		server.use(
			http.post(rpc("APIKeyService", "CreateAPIKey"), () =>
				HttpResponse.json({ plaintextKey: "sk_test_retry_secret" }),
			),
		);
		fireEvent.submit(
			within(dialog).getByRole("form", { name: "Create API key" }),
		);
		expect(await screen.findByText("sk_test_retry_secret")).toBeTruthy();
	});
	it("does not present a failed key list as an empty list", async () => {
		server.use(
			http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
				HttpResponse.json(
					{ code: "unavailable", message: "Service unavailable" },
					{ status: 503 },
				),
			),
		);
		renderInApp(<APIKeysPage />);
		expect((await screen.findByRole("alert")).textContent).toContain(
			"Couldn't load the API keys",
		);
	});
	it("renders the API keys the service returns for the active org", async () => {
		server.use(
			http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
				HttpResponse.json({
					keys: [
						{
							id: "key-1",
							organizationId: "org-1",
							userId: "user-1",
							name: "Production Backend",
							prefix: "sk_live_abcd",
							scopes: [{ resource: "*", action: "read" }],
							environment: 1,
							createdAt: "2026-01-02T03:04:05Z",
						},
					],
				}),
			),
		);
		renderInApp(<APIKeysPage />);
		expect(await screen.findByText("Production Backend")).toBeTruthy();
	});
});

it("hides key mutations when the current organization role cannot manage keys", async () => {
	auth.orgRole = "member";
	server.use(
		http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
			HttpResponse.json({
				keys: [{ id: "key-1", organizationId: "org-1", name: "Example key" }],
			}),
		),
	);
	renderInApp(<APIKeysPage />);
	await screen.findByText("Example key");
	expect(screen.queryByRole("button", { name: "Create key" })).toBeNull();
	expect(
		screen.queryByRole("button", { name: "Revoke Example key" }),
	).toBeNull();
});
