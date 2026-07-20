import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ThemePreference } from "@/gen/saas/accounts/v1/user_settings_pb";

const mocks = vi.hoisted(() => ({
	currentTheme: "system" as string,
	setTheme: vi.fn((theme: string) => {
		mocks.currentTheme = theme;
	}),
	update: vi.fn(async ({ theme }: { theme: ThemePreference }) => ({ theme })),
}));

vi.mock("next-themes", () => ({
	useTheme: () => ({
		theme: mocks.currentTheme,
		resolvedTheme: mocks.currentTheme === "dark" ? "dark" : "light",
		setTheme: mocks.setTheme,
	}),
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		user: { id: "user-1" },
		isAuthenticated: true,
		isLoading: false,
	}),
}));
vi.mock("../service/queries", () => ({
	userSettingsQueries: {
		current: () => ({
			queryKey: ["user-settings"],
			queryFn: async () => ({ theme: ThemePreference.DARK }),
		}),
	},
}));
vi.mock("../service/mutations", () => ({
	userSettingsMutations: { update: mocks.update },
}));

import {
	ThemePreferenceProvider,
	useThemePreference,
} from "./theme-preference-provider";

function Consumer() {
	const { preference, setPreference } = useThemePreference();
	return (
		<button type="button" onClick={() => void setPreference("light")}>
			{preference}
		</button>
	);
}

afterEach(() => {
	cleanup();
	mocks.currentTheme = "system";
	mocks.setTheme.mockClear();
	mocks.update.mockClear();
});

describe("ThemePreferenceProvider", () => {
	it("hydrates once from the account and persists a header selection", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={queryClient}>
				<ThemePreferenceProvider>
					<Consumer />
				</ThemePreferenceProvider>
			</QueryClientProvider>,
		);

		await waitFor(() => expect(mocks.setTheme).toHaveBeenCalledWith("dark"));
		mocks.setTheme.mockClear();
		fireEvent.click(screen.getByRole("button"));

		await waitFor(() =>
			expect(mocks.update).toHaveBeenCalledWith(
				{
					theme: ThemePreference.LIGHT,
				},
				expect.anything(),
			),
		);
		expect(mocks.setTheme).toHaveBeenCalledWith("light");
		expect(mocks.setTheme).not.toHaveBeenCalledWith("dark");
	});
});
