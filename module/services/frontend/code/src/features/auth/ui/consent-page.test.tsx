import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
	accessToken: "access-token" as string | null,
	isLoading: false,
	isAuthenticated: true,
	grantClientAuthorization: vi.fn(),
	declineClientAuthorization: vi.fn(),
}));

vi.mock("@/features/auth/model/client-authorization", async (original) => ({
	...(await original<Record<string, unknown>>()),
	grantClientAuthorization: h.grantClientAuthorization,
	declineClientAuthorization: h.declineClientAuthorization,
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		accessToken: h.accessToken,
		isLoading: h.isLoading,
		isAuthenticated: h.isAuthenticated,
	}),
}));

vi.mock("@/lib/appearance-provider", () => ({
	useAppearance: () => ({ branding: { name: "Example" } }),
}));

vi.mock("@/components/brand-mark", () => ({
	BrandMark: () => <div />,
}));

import {
	PENDING_REQUEST_KEY,
	PENDING_RESOLUTION_KEY,
} from "@/features/auth/model/client-authorization";
import { ConsentPage } from "./consent-page";

const request = {
	responseType: "code",
	clientId: "https://claude.ai/oauth/claude-code-client-metadata",
	redirectUri: "http://localhost:54321/callback",
	state: "opaque-state",
	codeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
	codeChallengeMethod: "S256",
	scope: "offline_access",
	resource: "https://host.example.com/solutions/example/mcp",
};

const resolution = {
	clientName: "Claude Code",
	clientUri: "https://claude.ai",
	clientSource: "metadata_document",
	resource: "https://host.example.com/solutions/example/mcp",
	resourceName: "example",
	scope: "offline_access",
	requiresConsent: true,
};

function storePending(
	pending: unknown = request,
	resolved: unknown = resolution,
): void {
	if (pending) {
		sessionStorage.setItem(PENDING_REQUEST_KEY, JSON.stringify(pending));
	}
	if (resolved) {
		sessionStorage.setItem(PENDING_RESOLUTION_KEY, JSON.stringify(resolved));
	}
}

beforeEach(() => {
	sessionStorage.clear();
	h.accessToken = "access-token";
	h.isLoading = false;
	h.isAuthenticated = true;
	h.grantClientAuthorization.mockReset().mockResolvedValue(undefined);
	h.declineClientAuthorization.mockReset();
});

afterEach(cleanup);

// Everything the page shows about the client comes from what the HOST said —
// the name and origin it resolved from the registry or from the document it
// fetched and validated. A page that rendered a name from the query string
// would let any site put any name in front of the person.
it("names the client, its origin and the resource from the host's own answer", () => {
	storePending();
	render(<ConsentPage />);

	expect(screen.getByText(/Allow Claude Code\?/)).toBeTruthy();
	expect(screen.getByText(/use example as you/)).toBeTruthy();
	expect(
		screen.getByText("https://host.example.com/solutions/example/mcp"),
	).toBeTruthy();
	// A client the operator never installed is said to be one, with the origin
	// the host verified.
	expect(screen.getByText(/not installed by your administrator/)).toBeTruthy();
	expect(screen.getByText("https://claude.ai")).toBeTruthy();
});

it("issues the code only when the person allows it", () => {
	storePending();
	render(<ConsentPage />);

	expect(h.grantClientAuthorization).not.toHaveBeenCalled();
	fireEvent.click(screen.getByRole("button", { name: /Allow/ }));
	expect(h.grantClientAuthorization).toHaveBeenCalledWith(
		expect.objectContaining({ clientId: request.clientId }),
		"access-token",
	);
});

// The client is sitting on a loopback listener. Told `access_denied` it can say
// so; left waiting it hangs until the person kills it.
it("tells the client when the person cancels", () => {
	storePending();
	render(<ConsentPage />);

	fireEvent.click(screen.getByRole("button", { name: /Cancel/ }));
	expect(h.declineClientAuthorization).toHaveBeenCalledWith(
		expect.objectContaining({ clientId: request.clientId }),
	);
	expect(h.grantClientAuthorization).not.toHaveBeenCalled();
});

// An operator-declared client is not flagged as uninstalled, and a request that
// narrows nothing names no resource.
it("says nothing about installation for a client the operator declared", () => {
	storePending(
		{ ...request, clientId: "example-addin", resource: "" },
		{
			clientName: "Example Add-in",
			clientSource: "registry",
			scope: "offline_access",
			requiresConsent: true,
		},
	);
	render(<ConsentPage />);

	expect(screen.getByText(/Allow Example Add-in\?/)).toBeTruthy();
	expect(screen.getByText(/asking to act as you/)).toBeTruthy();
	expect(screen.queryByText(/not installed by your administrator/)).toBeNull();
});

// Nothing to approve, or nobody to attribute the approval to. Both are the same
// thing from here, and saying so beats rendering an Allow button that cannot
// work.
it("offers no decision with no pending request", () => {
	render(<ConsentPage />);
	expect(screen.getByText(/no sign-in request to approve/)).toBeTruthy();
	expect(screen.queryByRole("button", { name: /Allow/ })).toBeNull();
});

it("offers no decision to a caller with no session", () => {
	storePending();
	h.isAuthenticated = false;
	render(<ConsentPage />);
	expect(screen.getByText(/no sign-in request to approve/)).toBeTruthy();
	expect(screen.queryByRole("button", { name: /Allow/ })).toBeNull();
});

it("waits while the session is still being established", () => {
	storePending();
	h.isLoading = true;
	render(<ConsentPage />);
	expect(screen.getByText(/Loading/)).toBeTruthy();
	expect(screen.queryByRole("button", { name: /Allow/ })).toBeNull();
});

// A failed grant leaves the person on the page with the reason, not navigated
// away with a client still waiting.
it("surfaces a failed grant instead of moving on", async () => {
	storePending();
	h.grantClientAuthorization.mockRejectedValue(
		new Error("could not be authorized"),
	);
	render(<ConsentPage />);

	fireEvent.click(screen.getByRole("button", { name: /Allow/ }));
	expect(await screen.findByText(/could not be authorized/)).toBeTruthy();
	expect(screen.getByRole("button", { name: /Allow/ })).toBeTruthy();
});
