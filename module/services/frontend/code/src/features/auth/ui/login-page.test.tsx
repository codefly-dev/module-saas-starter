import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/auth", () => ({
	availableProviders: () => [
		{
			id: "provider",
			displayName: "Provider",
			authorizeURL: "https://identity.example.test",
			clientID: "client",
			scope: "openid",
		},
	],
	useAuth: () => ({
		signInWith: vi.fn(),
		login: vi.fn(),
	}),
}));

vi.mock("@/lib/appearance-provider", () => ({
	useAppearance: () => ({
		branding: { name: "Example" },
	}),
}));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ push: vi.fn() }),
	useSearchParams: () => new URLSearchParams(),
}));

vi.mock("@/components/brand-mark", () => ({
	BrandMark: () => <div />,
}));

import { LoginPage } from "./login-page";

afterEach(cleanup);

describe("LoginPage", () => {
	it("does not present authentication as Terms acceptance", () => {
		render(<LoginPage />);

		expect(screen.getByText(/Continuing starts authentication/)).toBeTruthy();
		expect(screen.queryByText(/By continuing, you agree/)).toBeNull();
	});
});
