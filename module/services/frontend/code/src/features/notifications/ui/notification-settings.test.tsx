import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	read: vi.fn(async () => ({
		notifications: { inApp: true, push: true, sound: true },
	})),
	update: vi.fn(async (input: unknown) => input),
}));

vi.mock("@/features/user-settings/service/queries", () => ({
	userSettingsQueries: {
		current: () => ({
			queryKey: ["user-settings"],
			queryFn: mocks.read,
		}),
	},
}));

vi.mock("@/features/user-settings/service/mutations", () => ({
	userSettingsMutations: { update: mocks.update },
}));

import { NotificationSettings } from "./notification-settings";

afterEach(() => {
	cleanup();
	mocks.update.mockReset();
	mocks.read.mockReset();
	mocks.read.mockResolvedValue({
		notifications: { inApp: true, push: true, sound: true },
	});
});

describe("NotificationSettings", () => {
	it("shows only implemented behavior and saves only the in-app preference", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={queryClient}>
				<NotificationSettings />
			</QueryClientProvider>,
		);

		const inApp = await screen.findByRole("switch", {
			name: "Optional updates",
		});
		await waitFor(() => {
			expect(inApp.getAttribute("aria-disabled")).toBeNull();
		});
		expect(screen.queryByRole("switch", { name: "Push" })).toBeNull();
		expect(screen.queryByRole("switch", { name: "Sound" })).toBeNull();

		fireEvent.click(inApp);
		await waitFor(() => {
			expect(inApp.getAttribute("aria-checked")).toBe("false");
		});
		fireEvent.click(screen.getByRole("button", { name: "Save preferences" }));

		await waitFor(() =>
			expect(mocks.update).toHaveBeenCalledWith({
				patch: { notifications: { inApp: false } },
			}),
		);
	});
});

function mountSettings() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<NotificationSettings />
		</QueryClientProvider>,
	);
}
it("never displays a default preference for a refused read", async () => {
	mocks.read.mockRejectedValueOnce(
		new ConnectError("internal scope name", Code.PermissionDenied),
	);
	mountSettings();
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(screen.queryByRole("switch")).toBeNull();
	expect(screen.queryByText(/internal scope name/)).toBeNull();
	expect(
		(
			screen.getByRole("button", {
				name: "Save preferences",
			}) as HTMLButtonElement
		).disabled,
	).toBe(true);
});
it("locks a pending choice and retains it with a safe inline failure", async () => {
	let reject!: (reason: unknown) => void;
	mocks.update.mockImplementationOnce(
		() =>
			new Promise((_, fail) => {
				reject = fail;
			}),
	);
	mountSettings();
	const choice = await screen.findByRole("switch", {
		name: "Optional updates",
	});
	fireEvent.click(choice);
	fireEvent.click(screen.getByRole("button", { name: "Save preferences" }));
	await waitFor(() =>
		expect(choice.getAttribute("aria-disabled")).toBe("true"),
	);
	fireEvent.click(choice);
	expect(mocks.update).toHaveBeenCalledTimes(1);
	reject(new ConnectError("private server detail", Code.PermissionDenied));
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(choice.getAttribute("aria-checked")).toBe("false");
	expect(screen.queryByText(/private server detail/)).toBeNull();
});
