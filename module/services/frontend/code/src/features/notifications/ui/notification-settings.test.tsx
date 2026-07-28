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
	update: vi.fn(async (input: unknown) => input),
}));

vi.mock("@/features/user-settings/service/queries", () => ({
	userSettingsQueries: {
		current: () => ({
			queryKey: ["user-settings"],
			queryFn: async () => ({
				notifications: { inApp: true, push: true, sound: true },
			}),
		}),
	},
}));

vi.mock("@/features/user-settings/service/mutations", () => ({
	userSettingsMutations: { update: mocks.update },
}));

import { NotificationSettings } from "./notification-settings";

afterEach(() => {
	cleanup();
	mocks.update.mockClear();
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
		fireEvent.click(screen.getByRole("button", { name: "Save Preferences" }));

		await waitFor(() =>
			expect(mocks.update).toHaveBeenCalledWith({
				patch: { notifications: { inApp: false } },
			}),
		);
	});
});
