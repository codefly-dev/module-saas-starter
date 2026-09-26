import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	DatasourceAccountLinks,
	DatasourceDirectoryPanel,
} from "../directory.js";
import { createDatasourceClient } from "../gateway.js";
import type { DatasourceClient, DatasourceDirectoryView } from "../types.js";

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

function baseClient(overrides: Partial<DatasourceClient>): DatasourceClient {
	return {
		listSources: vi.fn(async () => []),
		addGitHubSource: vi.fn(async () => {}),
		syncSource: vi.fn(async () => "job-1"),
		deleteSource: vi.fn(async () => {}),
		...overrides,
	};
}

describe("DatasourceAccountLinks", () => {
	it("lists the person's link and starts a sign-in to link one", async () => {
		const assign = vi.fn();
		vi.stubGlobal("location", { ...window.location, assign });
		const client = baseClient({
			listMyAccountLinks: vi.fn(async () => [
				{
					id: "l1",
					userId: "u1",
					connector: "github",
					providerAccountId: "1001",
					providerAccountLogin: "jane",
				},
			]),
			beginAccountLink: vi.fn(async () => ({
				authorizeUrl: "https://provider.example.com/authorize",
				state: "dl.x",
			})),
			deleteAccountLink: vi.fn(async () => {}),
		});
		renderWithClient(<DatasourceAccountLinks client={client} orgId="org-1" />);

		expect(await screen.findByText("jane")).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: /link your github/i }));
		await waitFor(() =>
			expect(assign).toHaveBeenCalledWith(
				"https://provider.example.com/authorize",
			),
		);
		fireEvent.click(screen.getByRole("button", { name: /unlink/i }));
		await waitFor(() =>
			expect(client.deleteAccountLink).toHaveBeenCalledWith("org-1", "l1"),
		);
	});

	it("completes a link from the provider's return, once, and clears the address bar", async () => {
		window.history.replaceState(null, "", "/settings?state=dl.abc&code=xyz");
		const client = baseClient({
			listMyAccountLinks: vi.fn(async () => []),
			completeAccountLink: vi.fn(async () => ({
				id: "l1",
				userId: "u1",
				connector: "github",
				providerAccountId: "1001",
				providerAccountLogin: "jane",
			})),
		});
		renderWithClient(<DatasourceAccountLinks client={client} orgId="org-1" />);

		expect(await screen.findByText(/linked github account jane/i)).toBeTruthy();
		expect(client.completeAccountLink).toHaveBeenCalledTimes(1);
		expect(client.completeAccountLink).toHaveBeenCalledWith(
			"org-1",
			"dl.abc",
			"xyz",
		);
		expect(window.location.search).toBe("");
	});

	it("leaves another flow's return alone", async () => {
		// A GitHub App setup return carries a state that is not a link's.
		window.history.replaceState(null, "", "/settings?state=setup&code=xyz");
		const client = baseClient({
			listMyAccountLinks: vi.fn(async () => []),
			completeAccountLink: vi.fn(),
		});
		renderWithClient(<DatasourceAccountLinks client={client} orgId="org-1" />);
		expect(
			await screen.findByText(/no github account is linked/i),
		).toBeTruthy();
		expect(client.completeAccountLink).not.toHaveBeenCalled();
		expect(window.location.search).toBe("?state=setup&code=xyz");
	});
});

const directory: DatasourceDirectoryView = {
	links: [
		{
			id: "l1",
			userId: "u1",
			connector: "github",
			providerAccountId: "1001",
			providerAccountLogin: "jane",
		},
	],
	bindings: [
		{
			id: "b1",
			connector: "github",
			providerGroupId: "acme/platform",
			teamId: "t1",
		},
	],
	domains: [
		{
			id: "d1",
			domain: "example.com",
			verified: false,
			txtRecordName: "_saas-datasource-verification.example.com",
			txtRecordValue: "saas-datasource-verification=tok",
		},
	],
	teams: [
		{ id: "t1", name: "Platform" },
		{ id: "t2", name: "Support" },
	],
};

describe("DatasourceDirectoryPanel", () => {
	it("shows bindings, domains with their TXT record, and links", async () => {
		const client = baseClient({ getDirectory: vi.fn(async () => directory) });
		renderWithClient(
			<DatasourceDirectoryPanel client={client} orgId="org-1" />,
		);

		expect(await screen.findByText("acme/platform → Platform")).toBeTruthy();
		expect(screen.getByText("Pending")).toBeTruthy();
		expect(
			screen.getByText("_saas-datasource-verification.example.com"),
		).toBeTruthy();
		expect(screen.getByText("saas-datasource-verification=tok")).toBeTruthy();
		expect(screen.getByText("github jane")).toBeTruthy();
	});

	it("binds a group to a chosen team, and claims and verifies a domain", async () => {
		const client = baseClient({
			getDirectory: vi.fn(async () => directory),
			bindGroup: vi.fn(async () => ({
				id: "b2",
				connector: "github",
				providerGroupId: "acme/support",
				teamId: "t2",
			})),
			claimDomain: vi.fn(async () => directory.domains[0]),
			verifyDomain: vi.fn(async () => ({
				...directory.domains[0],
				verified: true,
			})),
		});
		renderWithClient(
			<DatasourceDirectoryPanel client={client} orgId="org-1" />,
		);
		await screen.findByText("acme/platform → Platform");

		const bind = screen.getByRole("button", {
			name: "Bind group",
		}) as HTMLButtonElement;
		expect(bind.disabled).toBe(true);
		fireEvent.change(screen.getByLabelText("Provider group"), {
			target: { value: "acme/support" },
		});
		fireEvent.change(screen.getByLabelText("Team"), {
			target: { value: "t2" },
		});
		fireEvent.click(bind);
		await waitFor(() =>
			expect(client.bindGroup).toHaveBeenCalledWith(
				"org-1",
				"github",
				"acme/support",
				"t2",
			),
		);

		fireEvent.change(screen.getByLabelText("Domain"), {
			target: { value: "example.org" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Claim domain" }));
		await waitFor(() =>
			expect(client.claimDomain).toHaveBeenCalledWith("org-1", "example.org"),
		);
		fireEvent.click(screen.getByRole("button", { name: "Verify" }));
		await waitFor(() => expect(client.verifyDomain).toHaveBeenCalled());
	});

	it("reports a refused change", async () => {
		const client = baseClient({
			getDirectory: vi.fn(async () => directory),
			unbindGroup: vi.fn(async () => {
				throw new Error("group binding not found");
			}),
		});
		renderWithClient(
			<DatasourceDirectoryPanel client={client} orgId="org-1" />,
		);
		fireEvent.click(await screen.findByRole("button", { name: "Unbind" }));
		expect(await screen.findByText("group binding not found")).toBeTruthy();
	});

	it("renders nothing for a client without the directory", () => {
		const { container } = renderWithClient(
			<DatasourceDirectoryPanel client={baseClient({})} orgId="org-1" />,
		);
		expect(container.textContent).toBe("");
	});
});

describe("the directory over the gateway", () => {
	const gatewayCatalog = JSON.parse(
		readFileSync(
			resolve(
				__dirname,
				"../../../../../../../accounts/generated/gateway-routes.json",
			),
			"utf8",
		),
	);
	const routable = new Set<string>(
		gatewayCatalog.routes
			.filter(
				(r: { protocol: string; method: string }) =>
					r.protocol === "GATEWAY_PROTOCOL_CONNECT" && r.method === "POST",
			)
			.map((r: { path: string }) => r.path),
	);

	it("reaches every directory procedure through a gateway route", async () => {
		const seen: string[] = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL) => {
				const url = String(input);
				const procedure = url.slice(url.lastIndexOf("/saas.accounts.v1."));
				seen.push(procedure);
				const body = procedure.endsWith("/GetDatasourceDirectory")
					? {
							links: [],
							bindings: [],
							domains: [
								{
									id: "d1",
									domain: "example.com",
									status: "DATASOURCE_DOMAIN_STATUS_VERIFIED",
								},
							],
							teams: [],
						}
					: procedure.endsWith("/BeginDatasourceAccountLink")
						? { authorizeUrl: "https://provider.example.com/a", state: "dl.s" }
						: procedure.endsWith("/CompleteDatasourceAccountLink")
							? {
									link: {
										id: "l1",
										connector: "github",
										providerAccountId: "1",
									},
								}
							: procedure.endsWith("/BindDatasourceGroup")
								? { binding: { id: "b1" } }
								: procedure.endsWith("Domain")
									? { domain: { id: "d1", domain: "example.com" } }
									: {};
				return new Response(JSON.stringify(body), {
					status: 200,
					headers: { "content-type": "application/json" },
				});
			}),
		);
		const client = createDatasourceClient({
			apiBase: "/api/solutions/guides/proxy",
			getAccessToken: () => "t",
		});
		await client.beginAccountLink!(
			"org-1",
			"github",
			"https://host.example.com/",
		);
		await client.completeAccountLink!("org-1", "dl.s", "c");
		await client.listMyAccountLinks!("org-1");
		await client.deleteAccountLink!("org-1", "l1");
		const dir = await client.getDirectory!("org-1");
		expect(dir.domains[0].verified).toBe(true);
		await client.bindGroup!("org-1", "github", "acme/x", "t1");
		await client.unbindGroup!("org-1", "b1");
		await client.claimDomain!("org-1", "example.com");
		await client.verifyDomain!("org-1", "d1");
		await client.deleteDomain!("org-1", "d1");
		expect(seen).toHaveLength(10);
		for (const procedure of seen) {
			expect(routable.has(procedure), `no gateway route for ${procedure}`).toBe(
				true,
			);
		}
	});
});
