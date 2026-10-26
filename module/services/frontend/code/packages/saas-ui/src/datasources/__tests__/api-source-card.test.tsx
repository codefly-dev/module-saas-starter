import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiSourceCard } from "../api-source-card.js";
import {
	apiSourceRefusal,
	matchPersonalApiSources,
	sourceLinks,
} from "../api-source.js";
import type {
	AccountLinkView,
	DatasourceClient,
	DatasourceView,
	SourceOperationDeclaration,
} from "../types.js";

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	window.history.replaceState(null, "", "/");
});

function renderWithClient(ui: ReactElement) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
	);
}

const api = { baseUrl: "https://api.example.com", name: "Example API" };

function apiSource(overrides: Partial<DatasourceView> = {}): DatasourceView {
	return {
		id: "ds-api",
		orgId: "org-1",
		provider: "api",
		repo: "",
		paths: [],
		branch: "",
		boundaryNodeId: "node-1",
		webhookConfigured: false,
		status: "active",
		lastSyncedAt: undefined,
		createdAt: undefined,
		api: {
			baseUrl: "https://api.example.com",
			resourcePath: "",
			credentialKind: "oauth2",
			oauth2Grant: "authorization_code",
		},
		...overrides,
	};
}

const link: AccountLinkView = {
	id: "l1",
	userId: "u1",
	connector: "api",
	providerAccountId: "source:ds-api",
	providerAccountLogin: "Connected API",
};

const operations: SourceOperationDeclaration[] = [
	{
		name: "list_records",
		method: "GET",
		path: "/records",
		inputSchema: { type: "object", additionalProperties: false },
		outputSchema: { type: "object" },
		effect: "read_only",
	},
];

function client(overrides: Partial<DatasourceClient>): DatasourceClient {
	return {
		listSources: vi.fn(async () => []),
		addGitHubSource: vi.fn(async () => {}),
		reconcileSource: vi.fn(),
		lookupReconcileSource: vi.fn(async () => undefined),
		deleteSource: vi.fn(async () => {}),
		listMyAccountLinks: vi.fn(async () => []),
		beginAccountLink: vi.fn(async () => ({
			authorizeUrl: "https://provider.example.com/authorize",
			state: "dl.x",
		})),
		completeAccountLink: vi.fn(),
		addPersonalApiSource: vi.fn(async () => apiSource()),
		declareSourceOperations: vi.fn(async () => {}),
		...overrides,
	};
}

describe("ApiSourceCard", () => {
	it("creates the person's source, declares its operations, and signs in for it", async () => {
		const assign = vi.fn();
		vi.stubGlobal("location", { ...window.location, assign });
		const c = client({});
		renderWithClient(
			<ApiSourceCard
				client={c}
				orgId="org-1"
				api={{ ...api, collection: "Records" }}
				operations={operations}
			/>,
		);

		expect(await screen.findByText("Set up")).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Connect Example API" }));

		await waitFor(() =>
			expect(assign).toHaveBeenCalledWith(
				"https://provider.example.com/authorize",
			),
		);
		expect(c.addPersonalApiSource).toHaveBeenCalledWith({
			orgId: "org-1",
			baseUrl: "https://api.example.com",
			resourcePath: "",
			targetCollection: "Records",
		});
		expect(c.declareSourceOperations).toHaveBeenCalledWith(
			"org-1",
			"ds-api",
			operations,
		);
		expect(c.beginAccountLink).toHaveBeenCalledWith(
			"org-1",
			"api",
			`${window.location.origin}${window.location.pathname}`,
			"ds-api",
		);
	});

	it("declares nothing when it is given no operations", async () => {
		vi.stubGlobal("location", { ...window.location, assign: vi.fn() });
		const c = client({});
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Connect Example API" }),
		);
		await waitFor(() => expect(c.beginAccountLink).toHaveBeenCalled());
		expect(c.declareSourceOperations).not.toHaveBeenCalled();
	});

	it("offers the sign-in for a source the person already has, rather than a second source", async () => {
		const assign = vi.fn();
		vi.stubGlobal("location", { ...window.location, assign });
		const c = client({ listSources: vi.fn(async () => [apiSource()]) });
		renderWithClient(
			<ApiSourceCard
				client={c}
				orgId="org-1"
				api={api}
				operations={operations}
			/>,
		);

		expect(await screen.findByText("Sign-in needed")).toBeTruthy();
		expect(screen.queryByRole("button", { name: /^connect/i })).toBeNull();
		fireEvent.click(
			await screen.findByRole("button", { name: "Sign in to Example API" }),
		);
		await waitFor(() => expect(assign).toHaveBeenCalled());
		expect(c.addPersonalApiSource).not.toHaveBeenCalled();
		// A declaration refused before is declared again on the next sign-in.
		expect(c.declareSourceOperations).toHaveBeenCalledWith(
			"org-1",
			"ds-api",
			operations,
		);
		expect(c.beginAccountLink).toHaveBeenCalledWith(
			"org-1",
			"api",
			expect.any(String),
			"ds-api",
		);
	});

	it("reads as connected once the person has signed in for the source", async () => {
		const c = client({
			listSources: vi.fn(async () => [apiSource()]),
			listMyAccountLinks: vi.fn(async () => [link]),
		});
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		expect(await screen.findByText("Connected")).toBeTruthy();
		expect(screen.getByRole("button", { name: "Sign in again" })).toBeTruthy();
	});

	it("completes the provider's return on the page it comes back to", async () => {
		window.history.replaceState(null, "", "/connect?state=dl.abc&code=xyz");
		const c = client({
			listSources: vi.fn(async () => [apiSource()]),
			listMyAccountLinks: vi.fn(async () => [link]),
			completeAccountLink: vi.fn(async () => link),
		});
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		expect(await screen.findByText("Signed in to Example API.")).toBeTruthy();
		expect(c.completeAccountLink).toHaveBeenCalledWith("org-1", "dl.abc", "xyz");
		expect(window.location.search).toBe("");
	});

	it("does not treat another API's source, or a shared one, as the person's", async () => {
		const c = client({
			listSources: vi.fn(async () => [
				apiSource({
					id: "other",
					api: { ...apiSource().api!, baseUrl: "https://other.example.com" },
				}),
				apiSource({
					id: "shared",
					api: { ...apiSource().api!, oauth2Grant: "client_credentials" },
				}),
			]),
		});
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		expect(await screen.findByText("Set up")).toBeTruthy();
	});

	it("reports two sources for one API rather than picking one", async () => {
		const c = client({
			listSources: vi.fn(async () => [
				apiSource(),
				apiSource({ id: "ds-api-2" }),
			]),
		});
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		expect(await screen.findByText("More than one source")).toBeTruthy();
		expect(screen.getByText(/ds-api-2/)).toBeTruthy();
		expect(screen.queryByRole("button", { name: /sign in/i })).toBeNull();
	});

	it("says what to do when the deployment has no sign-in for the API", async () => {
		const c = client({
			addPersonalApiSource: vi.fn(async () => {
				throw new ConnectError(
					"provider sign-in is not configured",
					Code.FailedPrecondition,
				);
			}),
		});
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Connect Example API" }),
		);
		expect(
			await screen.findByText(
				/no sign-in configured for Example API.*datasource-oauth configuration/,
			),
		).toBeTruthy();
		expect(c.beginAccountLink).not.toHaveBeenCalled();
	});

	it("keeps a source it created when a later step is refused, and offers its sign-in", async () => {
		const sources: DatasourceView[] = [];
		const c = client({
			listSources: vi.fn(async () => [...sources]),
			addPersonalApiSource: vi.fn(async () => {
				const created = apiSource();
				sources.push(created);
				return created;
			}),
			declareSourceOperations: vi.fn(async () => {
				throw new ConnectError(
					"organization administrator required",
					Code.PermissionDenied,
				);
			}),
		});
		renderWithClient(
			<ApiSourceCard
				client={c}
				orgId="org-1"
				api={api}
				operations={operations}
			/>,
		);
		fireEvent.click(
			await screen.findByRole("button", { name: "Connect Example API" }),
		);
		expect(
			await screen.findByText(/needs the administrator or owner role/),
		).toBeTruthy();
		expect(await screen.findByText("Sign-in needed")).toBeTruthy();
		expect(c.addPersonalApiSource).toHaveBeenCalledTimes(1);
		expect(c.beginAccountLink).not.toHaveBeenCalled();
	});

	it("offers no connect a client cannot finish", async () => {
		const c = client({ beginAccountLink: undefined });
		renderWithClient(<ApiSourceCard client={c} orgId="org-1" api={api} />);
		expect(
			await screen.findByText("This client cannot connect an API."),
		).toBeTruthy();
	});
});

describe("matchPersonalApiSources", () => {
	it("matches on the base URL and resource path, trimmed, and on the grant", () => {
		const mine = apiSource();
		const withPath = apiSource({
			id: "p",
			api: { ...mine.api!, resourcePath: "/v1/records" },
		});
		const sources = [mine, withPath, apiSource({ id: "g", provider: "github" })];
		expect(
			matchPersonalApiSources({ baseUrl: " https://api.example.com " }, sources),
		).toEqual([mine]);
		expect(
			matchPersonalApiSources(
				{ baseUrl: "https://api.example.com", resourcePath: "/v1/records" },
				sources,
			),
		).toEqual([withPath]);
		// A trailing slash is a different base path to the host.
		expect(
			matchPersonalApiSources({ baseUrl: "https://api.example.com/" }, sources),
		).toEqual([]);
	});

	it("finds a source's link by the account the host names after it", () => {
		expect(
			sourceLinks(
				[link, { ...link, id: "x", connector: "github" }],
				"ds-api",
			),
		).toEqual([link]);
	});
});

describe("apiSourceRefusal", () => {
	it.each([
		["provider sign-in is not configured", /no sign-in configured for X/],
		[
			"This deployment cannot link api accounts: no sign-in with that provider is configured.",
			/no sign-in configured for X/,
		],
		["requires org admin or owner role", /administrator or owner role/],
		["source permission required", /access to the collection/],
		["source is not owned by this member", /another member/],
	])("words %s as something to do", (raw, words) => {
		expect(
			apiSourceRefusal(new ConnectError(raw, Code.PermissionDenied), "X"),
		).toMatch(words);
	});

	it("keeps the host's own words for anything else, without the envelope", () => {
		expect(
			apiSourceRefusal(
				new Error("rpc error: code = Unavailable desc = Retry shortly."),
				"X",
			),
		).toBe("Retry shortly.");
	});
});
