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
	query: new URLSearchParams(),
	validateClientAuthorization: vi.fn(),
	rememberClientAuthorization: vi.fn(),
	forgetClientAuthorization: vi.fn(),
}));

vi.mock("@/features/auth/model/client-authorization", async (original) => ({
	...(await original<Record<string, unknown>>()),
	validateClientAuthorization: h.validateClientAuthorization,
	rememberClientAuthorization: h.rememberClientAuthorization,
	forgetClientAuthorization: h.forgetClientAuthorization,
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
	useSearchParams: () => h.query,
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
	h.query = new URLSearchParams();
	h.validateClientAuthorization.mockReset();
	h.rememberClientAuthorization.mockReset();
	h.forgetClientAuthorization.mockReset();
});

// A registered client's authorization request, as it arrives in the login
// page's query string.
function clientQuery(overrides: Record<string, string> = {}) {
	return new URLSearchParams({
		client_id: "example-addin",
		redirect_uri: "https://localhost:3000/auth/callback",
		state: "opaque-state",
		code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		code_challenge_method: "S256",
		...overrides,
	});
}

afterEach(cleanup);

describe("LoginPage", () => {
	it("does not present authentication as Terms acceptance", () => {
		render(<LoginPage identity={{}} />);

		expect(screen.getByText(/Continuing starts authentication/)).toBeTruthy();
		expect(screen.queryByText(/By continuing, you agree/)).toBeNull();
	});

	it("exchanges the injected header on load and redirects, with no provider button", async () => {
		h.headerInjected = true;
		h.providers = [];
		h.loginWithHeaderInjected.mockResolvedValue(true);

		render(<LoginPage identity={{}} />);

		expect(screen.getByText(/Signing you in/)).toBeTruthy();
		expect(h.loginWithHeaderInjected).toHaveBeenCalledTimes(1);
		await waitFor(() => expect(h.push).toHaveBeenCalledTimes(1));
	});

	it("surfaces a message when the provider begin call fails, instead of a dead button", async () => {
		h.signInWith.mockRejectedValue(new Error("BeginOAuth failed: 500"));

		render(<LoginPage identity={{}} />);

		fireEvent.click(
			screen.getByRole("button", { name: /Continue with Provider/ }),
		);

		await waitFor(() =>
			expect(screen.getByText(/temporarily misconfigured/)).toBeTruthy(),
		);
		expect(h.push).not.toHaveBeenCalled();
	});

	it("names the client once the host has accepted its request", async () => {
		h.query = clientQuery();
		h.validateClientAuthorization.mockResolvedValue("Example Add-in");

		render(<LoginPage identity={{}} />);

		await waitFor(() =>
			expect(screen.getByText(/to continue to Example Add-in/)).toBeTruthy(),
		);
		expect(h.rememberClientAuthorization).toHaveBeenCalledTimes(1);
		expect(
			screen.getByRole("button", { name: /Continue with Provider/ }),
		).toBeTruthy();
	});

	it("offers no way to sign in until the host has accepted the client", () => {
		h.query = clientQuery();
		h.validateClientAuthorization.mockReturnValue(new Promise(() => {}));

		render(<LoginPage identity={{}} />);

		expect(screen.getByText(/Checking the application/)).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: /Continue with Provider/ }),
		).toBeNull();
	});

	it("refuses an unregistered client before offering any sign-in method", async () => {
		h.query = clientQuery({ redirect_uri: "https://evil.test/auth/callback" });
		h.validateClientAuthorization.mockRejectedValue(
			new Error("This application is not registered to sign in here."),
		);

		render(<LoginPage identity={{}} />);

		await waitFor(() =>
			expect(screen.getByText(/not registered to sign in here/)).toBeTruthy(),
		);
		expect(
			screen.queryByRole("button", { name: /Continue with Provider/ }),
		).toBeNull();
		expect(h.rememberClientAuthorization).not.toHaveBeenCalled();
		expect(h.forgetClientAuthorization).toHaveBeenCalled();
	});

	it("does not sign a header-injected person in behind a refused client", async () => {
		h.headerInjected = true;
		h.providers = [];
		h.query = clientQuery({ client_id: "nobody" });
		h.validateClientAuthorization.mockRejectedValue(new Error("refused"));

		render(<LoginPage identity={{}} />);

		await waitFor(() => expect(screen.getByText(/refused/)).toBeTruthy());
		expect(h.loginWithHeaderInjected).not.toHaveBeenCalled();
		expect(h.push).not.toHaveBeenCalled();
	});

	it("surfaces a denied group gate instead of redirecting", async () => {
		h.headerInjected = true;
		h.providers = [];
		h.loginWithHeaderInjected.mockRejectedValue(
			new Error("Access not granted for your account."),
		);

		render(<LoginPage identity={{}} />);

		await waitFor(() =>
			expect(screen.getByText(/Access not granted/)).toBeTruthy(),
		);
		expect(h.push).not.toHaveBeenCalled();
	});
});
