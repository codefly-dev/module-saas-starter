import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({ profile: {} as Record<string, string> }));

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
					profile: state.profile,
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

import { userProfileMutations } from "@/features/user-profile/service/mutations";
import { userSettingsMutations } from "../service/mutations";
import { GeneralSettingsPage } from "./general-settings-page";

afterEach(cleanup);
beforeEach(() => {
	vi.clearAllMocks();
	state.profile = {};
});

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

function renderSettings() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<GeneralSettingsPage />
		</QueryClientProvider>,
	);
	return client;
}

it("retains another section's draft when saving and refreshing the profile", async () => {
	const client = renderSettings();
	const timezone = await screen.findByRole("textbox", { name: "Time zone" });
	fireEvent.change(timezone, { target: { value: "Europe/Paris" } });
	fireEvent.change(screen.getByRole("textbox", { name: "Full name" }), {
		target: { value: "Jane Doe" },
	});
	vi.mocked(userProfileMutations.update).mockImplementationOnce(async () => {
		state.profile = { name: "Jane Doe" };
		return undefined as never;
	});
	fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
	await waitFor(() =>
		expect(userProfileMutations.update).toHaveBeenCalledOnce(),
	);
	await act(async () => {
		await client.invalidateQueries({ queryKey: ["user-profile"] });
	});
	expect(
		(screen.getByRole("textbox", { name: "Time zone" }) as HTMLInputElement)
			.value,
	).toBe("Europe/Paris");
});

it("keeps fields disabled during a save and retains a safe failure beside the draft", async () => {
	let reject!: (error: Error) => void;
	vi.mocked(userProfileMutations.update).mockImplementationOnce(
		() =>
			new Promise((_, fail) => {
				reject = fail;
			}),
	);
	renderSettings();
	const name = await screen.findByRole("textbox", { name: "Full name" });
	fireEvent.change(name, { target: { value: "Jane Doe" } });
	fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
	await waitFor(() => expect(name.hasAttribute("disabled")).toBe(true));
	expect(
		screen
			.getByRole("button", { name: "Save email preferences" })
			.hasAttribute("disabled"),
	).toBe(true);
	await act(async () => reject(new Error("private internal diagnostic")));
	expect((await screen.findByRole("alert")).textContent).toContain(
		"Couldn't save your changes.",
	);
	expect(screen.queryByText("private internal diagnostic")).toBeNull();
	expect((name as HTMLInputElement).value).toBe("Jane Doe");
	expect(name.hasAttribute("disabled")).toBe(false);
});

it("validates a time zone and profile picture link beside their fields", async () => {
	renderSettings();
	const timezone = await screen.findByRole("textbox", { name: "Time zone" });
	fireEvent.change(timezone, { target: { value: "not-a-time-zone" } });
	fireEvent.click(screen.getByRole("button", { name: "Save time & date" }));
	expect(timezone.getAttribute("aria-invalid")).toBe("true");
	expect(userSettingsMutations.update).not.toHaveBeenCalled();
	const picture = screen.getByRole("textbox", { name: "Profile picture link" });
	fireEvent.change(picture, { target: { value: "not-a-link" } });
	fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
	expect(picture.getAttribute("aria-invalid")).toBe("true");
	expect(userProfileMutations.update).not.toHaveBeenCalled();
});
