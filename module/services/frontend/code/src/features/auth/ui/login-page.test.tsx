import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
	headerInjected: false,
	providers: [] as unknown[],
	signInWith: vi.fn(),
	loginWithHeaderInjected: vi.fn(),
	push: vi.fn(),
}));

vi.mock("@/lib/auth", () => ({
	availableProviders: () => h.providers,
	isHeaderInjectedProvider: () => h.headerInjected,
	useAuth: () => ({
		signInWith: h.signInWith,
		login: vi.fn(),
		loginWithHeaderInjected: h.loginWithHeaderInjected,
	}),
}));

vi.mock("@/lib/appearance-provider", () => ({
	useAppearance: () => ({
		branding: { name: "Example" },
	}),
}));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ push: h.push }),
	useSearchParams: () => new URLSearchParams(),
}));

vi.mock("@/components/brand-mark", () => ({
	BrandMark: () => <div />,
}));

import { LoginPage } from "./login-page";

beforeEach(() => {
	h.headerInjected = false;
	h.providers = [
		{
			id: "provider",
			displayName: "Provider",
			authorizeURL: "https://identity.example.test",
			clientID: "client",
			scope: "openid",
		},
	];
	h.signInWith.mockReset();
	h.signInWith.mockResolvedValue(undefined);
	h.loginWithHeaderInjected.mockReset();
	h.push.mockReset();
});

afterEach(cleanup);

describe("LoginPage", () => {
	it("does not present authentication as Terms acceptance", () => {
		render(<LoginPage />);

		expect(screen.getByText(/Continuing starts authentication/)).toBeTruthy();
		expect(screen.queryByText(/By continuing, you agree/)).toBeNull();
	});

	it("exchanges the injected header on load and redirects, with no provider button", async () => {
		h.headerInjected = true;
		h.providers = [];
		h.loginWithHeaderInjected.mockResolvedValue(true);

		render(<LoginPage />);

		expect(screen.getByText(/Signing you in/)).toBeTruthy();
		expect(h.loginWithHeaderInjected).toHaveBeenCalledTimes(1);
		await waitFor(() => expect(h.push).toHaveBeenCalledTimes(1));
	});

	it("surfaces a message when the provider begin call fails, instead of a dead button", async () => {
		h.signInWith.mockRejectedValue(new Error("BeginOAuth failed: 500"));

		render(<LoginPage />);

		fireEvent.click(
			screen.getByRole("button", { name: /Continue with Provider/ }),
		);

		await waitFor(() =>
			expect(screen.getByText(/temporarily misconfigured/)).toBeTruthy(),
		);
		expect(h.push).not.toHaveBeenCalled();
	});

	it("surfaces a denied group gate instead of redirecting", async () => {
		h.headerInjected = true;
		h.providers = [];
		h.loginWithHeaderInjected.mockRejectedValue(
			new Error("Access not granted for your account."),
		);

		render(<LoginPage />);

		await waitFor(() =>
			expect(screen.getByText(/Access not granted/)).toBeTruthy(),
		);
		expect(h.push).not.toHaveBeenCalled();
	});
});
