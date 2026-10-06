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
	// The origin the host VERIFIED by fetching the document, never the
	// document's own `client_uri` claim.
	clientOrigin: "https://claude.ai",
	clientSource: "metadata_document",
	resource: "https://host.example.com/solutions/example/mcp",
	resourceName: "example",
	scope: "offline_access",
	requiresConsent: true,
	issuer: "https://host.example.com",
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
	// The verified origin, shown in both places: as the identity under the
	// client's own (unverified) name, and in the warning.
	expect(screen.getAllByText("https://claude.ai").length).toBeGreaterThan(0);
});

// A1007-06. A document claiming a trusted-looking `client_uri` must not be able
// to put that origin in front of the person: the host never reads the claim, so
// the page has only the verified origin to show. Adopted from the Astra review.
it("shows only the origin the host verified, never a claimed one", () => {
	storePending(request, {
		...resolution,
		clientName: "Totally Trusted",
		clientOrigin: "https://client.example.com",
	});
	render(<ConsentPage />);

	expect(
		screen.getAllByText("https://client.example.com").length,
	).toBeGreaterThan(0);
	expect(screen.queryByText(/trusted\.example\.com/)).toBeNull();
});

it("issues the code only when the person allows it", () => {
	storePending();
	render(<ConsentPage />);

	expect(h.grantClientAuthorization).not.toHaveBeenCalled();
	fireEvent.click(screen.getByRole("button", { name: /Allow/ }));
	// And it states that the person approved — the one call site allowed to.
	// The host refuses a grant that needs consent without it, so this argument
	// is what makes the screen load-bearing rather than decorative.
	expect(h.grantClientAuthorization).toHaveBeenCalledWith(
		expect.objectContaining({ clientId: request.clientId }),
		"access-token",
		{ consentGranted: true },
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
			clientOrigin: "example-addin",
			clientSource: "registry",
			scope: "offline_access",
			requiresConsent: true,
			issuer: "https://host.example.com",
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

// A1007B-06. A pending request whose resolution is absent or no longer
// decodable — an in-flight sessionStorage record written by an older response
// shape across a deployment — must NOT render Allow. Without the resolution the
// page has no verified origin, no resource and no uninstalled-client warning to
// show, and an approval given against a blank presentation approves nothing.
//
// Adopted from the Astra review (missing resolution consent page continuation).
it("offers no decision without a validated resolution", () => {
	storePending(request, null);
	render(<ConsentPage />);

	expect(screen.queryByRole("button", { name: /Allow/ })).toBeNull();
	expect(screen.queryByRole("button", { name: /Cancel/ })).toBeNull();
	expect(screen.getByText(/could not be confirmed/)).toBeTruthy();
	expect(h.grantClientAuthorization).not.toHaveBeenCalled();
});

// Including one that is present but undecodable, which is the shape an older
// deployment's record actually takes.
it("offers no decision when the stored resolution cannot be decoded", () => {
	storePending(request, null);
	sessionStorage.setItem(
		"client_authorization_resolution",
		JSON.stringify({ clientName: "Claude Code", requiresConsent: true }),
	);
	render(<ConsentPage />);

	expect(screen.queryByRole("button", { name: /Allow/ })).toBeNull();
	expect(screen.getByText(/could not be confirmed/)).toBeTruthy();
});
