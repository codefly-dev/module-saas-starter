import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../service/queries", () => ({
	userSettingsQueries: {
		current: () => ({
			queryKey: ["user-settings"],
			queryFn: async () => ({}),
		}),
	},
}));

vi.mock("@/features/user-profile/service/queries", () => ({
	userProfileQueries: {
		current: () => ({
			queryKey: ["user-profile"],
			queryFn: async () => ({
				user: {
					uuid: "user-1",
					primaryEmail: "user@example.com",
					profile: {},
				},
			}),
		}),
	},
}));

vi.mock("../service/mutations", () => ({
	userSettingsMutations: { update: vi.fn() },
}));

vi.mock("@/features/user-profile/service/mutations", () => ({
	userProfileMutations: { update: vi.fn() },
}));

vi.mock("./theme-preference-provider", () => ({
	useThemePreference: () => ({
		preference: "system",
		isSaving: false,
		setPreference: vi.fn(),
	}),
}));

import { GeneralSettingsPage } from "./general-settings-page";

afterEach(cleanup);

describe("GeneralSettingsPage email capabilities", () => {
	it("shows only email behavior backed by a producer", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={queryClient}>
				<GeneralSettingsPage />
			</QueryClientProvider>,
		);

		expect(await screen.findByText("Email preferences")).not.toBeNull();
		expect(
			screen.getByText(/Billing lifecycle messages are always on/),
		).not.toBeNull();
		expect(screen.queryByText("Security alerts")).toBeNull();
		expect(screen.queryByText(/Sign-in from a new device/)).toBeNull();
	});
});
