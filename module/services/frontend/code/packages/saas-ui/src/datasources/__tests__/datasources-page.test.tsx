import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import type {
	CollectionAccessView,
	DatasourceClient,
	DatasourceProviderView,
	DatasourceView,
} from "../types.js";

// What an organization administrator reads on the Data sources page: their
// sources first, then how to connect another, and who can read each one. The
// page never shows an identifier or a word that only makes sense to whoever
// built the host.

afterEach(cleanup);

const COLLECTION = "0196a1b2-7c3d-7e4f-8a5b-6c7d8e9f0a1b";
const SOURCE = "0196a1b2-7c3d-7e4f-8a5b-000000000001";
const JOB = "0196a1b2-7c3d-7e4f-8a5b-00000000aaaa";
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;
/** Words that name how the host works rather than what a person does. */
const INTERNAL =
	/stopgap|effect identity|reconcil|boundary|connector|envelope|conformant|dispatch|node id|\bjob\b|scope path|untyped|cursor|ingestion/i;

const github: DatasourceProviderView = {
	provider: 1,
	connector: "github",
	displayName: "GitHub",
	description: "A GitHub repository, pulled on sync.",
	supportsOrganizationAppSetup: true,
	acceptsNewSources: true,
	gap: "",
	supportsWebhook: true,
	credentialModes: ["org_app", "static_secret", "none"],
	fields: [
		{
			key: "repo",
			displayName: "Repository",
			help: "",
			required: true,
			format: "text",
		},
		{
			key: "branch",
			displayName: "Branch",
			help: "",
			required: false,
			format: "text",
		},
	],
};
const objectStorage: DatasourceProviderView = {
	provider: 4,
	connector: "upload",
	displayName: "Object storage",
	description: "A bucket in an object store.",
	acceptsNewSources: true,
	gap: "",
	supportsWebhook: false,
	credentialModes: ["static_secret"],
	fields: [
		{
			key: "bucket",
			displayName: "Bucket",
			help: "",
			required: true,
			format: "text",
		},
	],
};
const retired: DatasourceProviderView[] = [
	{
		...objectStorage,
		provider: 2,
		connector: "api",
		displayName: "HTTP API",
		acceptsNewSources: false,
		gap: "one untyped response with no item identity, no cursor and no deletions; to be rebuilt as a records connector or retired",
	},
	{
		...objectStorage,
		provider: 3,
		connector: "crawler",
		displayName: "Web crawler",
		acceptsNewSources: false,
		gap: "no cursor and no deletions (a page leaving the sitemap is never removed), and one fetch per page",
	},
];

const handbook: DatasourceView = {
	id: SOURCE,
	orgId: "0196a1b2-7c3d-7e4f-8a5b-0000000000f0",
	provider: "github",
	repo: "acme/handbook",
	paths: ["docs/"],
	fileExtensions: [".md"],
	branch: "main",
	boundaryNodeId: COLLECTION,
	boundaryLabel: "Handbook",
	webhookConfigured: false,
	liveDelivery: "app_webhook",
	reconcileIntervalSeconds: 21600,
	status: "active",
	lastSyncedAt: undefined,
	lastIngestedAt: "2026-11-16T09:30:00Z",
	lastIngestedCommit: "9f8e7d6c5b4a",
	createdAt: undefined,
};

const handbookCollection: CollectionAccessView = {
	nodeId: COLLECTION,
	label: "Handbook",
	scopePath: `org_${COLLECTION.replaceAll("-", "_")}`,
	grants: [
		{
			id: "grant-1",
			subjectId: "team-1",
			subjectKind: "team",
			scopePath: `org_${COLLECTION.replaceAll("-", "_")}`,
			roleId: "role-1",
			subjectLabel: "Engineering",
			roleName: "collection-reader",
			actorLabel: "Alice Johnson",
		},
	],
};

function client(overrides: Partial<DatasourceClient> = {}): DatasourceClient {
	return {
		getCatalog: vi.fn(async () => [github, objectStorage, ...retired]),
		addSource: vi.fn(async () => {}),
		listSources: vi.fn(async () => [handbook]),
		listCollections: vi.fn(async () => [handbookCollection]),
		listGrantSubjects: vi.fn(async () => [
			{ id: "team-1", kind: "team" as const, label: "Engineering" },
		]),
		grantCollectionRead: vi.fn(async () => {}),
		revokeCollectionRead: vi.fn(async () => {}),
		listAccessibleScopes: vi.fn(async () => [
			{
				nodeId: COLLECTION,
				label: "Handbook",
				kind: "collection",
				actions: ["read"],
			},
		]),
		addGitHubSource: vi.fn(async () => {}),
		reconcileSource: vi.fn(async () => ({
			effectId: JOB.replace("aaaa", "bbbb"),
			status: "committed" as const,
			jobId: JOB,
		})),
		lookupReconcileSource: vi.fn(async () => undefined),
		updateSource: vi.fn(async () => undefined),
		deleteSource: vi.fn(async () => {}),
		beginGitHubAppSetup: vi.fn(async () => ({
			installUrl: "https://github.com/apps/example/installations/new?state=s1",
			state: "s1",
			expiresAt: undefined,
		})),
		completeGitHubAppSetup: vi.fn(async () => ({
			installationId: "42",
			repositories: [],
		})),
		...overrides,
	};
}

function renderPanel(ui: ReactElement) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
	);
}

async function sourceRow(name: string) {
	const list = await screen.findByRole("list", { name: "Connected sources" });
	const row = (await within(list).findByText(name)).closest("li");
	if (!row) throw new Error(`no row for ${name}`);
	return row as HTMLElement;
}

async function openGitHubForm() {
	const connect = await screen.findByRole("region", {
		name: "Connect a source",
	});
	fireEvent.click(
		await within(connect).findByRole("radio", { name: "GitHub" }),
	);
	return connect;
}

describe("Data sources page", () => {
	it("starts with the connected sources, each with its provider, contents, last sync and status", async () => {
		renderPanel(<DatasourcesPanel client={client()} orgId="org-1" />);
		const row = await sourceRow("acme/handbook");
		const headings = screen.getAllByRole("heading");
		expect(headings[0]).toHaveTextContent("Connected sources");
		expect(row).toHaveTextContent("GitHub");
		expect(row).toHaveTextContent("docs/");
		expect(row).toHaveTextContent(".md");
		expect(row).toHaveTextContent("main");
		expect(row).toHaveTextContent(/Synced/);
		expect(row).toHaveTextContent("Active");
		for (const action of ["Sync now", "Edit", "Remove"])
			expect(
				within(row).getByRole("button", {
					name: new RegExp(`^${action} acme/handbook$`),
				}),
			).toBeVisible();
	});

	it("says who can read each source inline, and that connecting shares nothing", async () => {
		const unread: DatasourceView = {
			...handbook,
			id: "src-2",
			repo: "acme/runbooks",
			boundaryNodeId: "c-2",
			boundaryLabel: "Runbooks",
		};
		renderPanel(
			<DatasourcesPanel
				client={client({
					listSources: vi.fn(async () => [handbook, unread]),
					listCollections: vi.fn(async () => [
						handbookCollection,
						{ nodeId: "c-2", label: "Runbooks", scopePath: "x", grants: [] },
					]),
				})}
				orgId="org-1"
			/>,
		);
		const read = await sourceRow("acme/handbook");
		await waitFor(() => expect(read).toHaveTextContent("Engineering"));
		expect(read).toHaveTextContent("Who can read it");
		const nobody = await sourceRow("acme/runbooks");
		expect(nobody).toHaveTextContent(
			"Connecting doesn't share anything; choose who can read it",
		);
		fireEvent.click(
			within(nobody).getByRole("button", {
				name: "Choose who can read acme/runbooks",
			}),
		);
		expect(
			await within(nobody).findByRole("listbox", { name: "People and teams" }),
		).toBeVisible();
	});

	it("names the providers to connect when there is no source yet", async () => {
		renderPanel(
			<DatasourcesPanel
				client={client({ listSources: vi.fn(async () => []) })}
				orgId="org-1"
			/>,
		);
		expect(await screen.findByText("No sources yet")).toBeVisible();
		expect(
			await screen.findByText(/connect GitHub or object storage/i),
		).toBeVisible();
	});

	it("hides providers that take no new sources, and every note about them", async () => {
		renderPanel(<DatasourcesPanel client={client()} orgId="org-1" />);
		const connect = await screen.findByRole("region", {
			name: "Connect a source",
		});
		expect(
			await within(connect).findByRole("radio", { name: "GitHub" }),
		).toBeVisible();
		expect(
			within(connect).getByRole("radio", { name: "Object storage" }),
		).toBeVisible();
		expect(screen.queryByText(/HTTP API|Web crawler/)).toBeNull();
		expect(document.body.textContent).not.toMatch(
			/Not accepting new sources|untyped|sitemap/,
		);
	});

	it("labels every field of the GitHub form and explains each way to connect", async () => {
		renderPanel(<DatasourcesPanel client={client()} orgId="org-1" />);
		const form = await openGitHubForm();
		for (const label of [
			"Repository",
			"Branch",
			"Folders",
			"File types",
			"Collection",
		])
			expect(within(form).getByLabelText(label)).toBeVisible();
		const token = within(form).getByRole("radio", { name: /Access token/ });
		const app = within(form).getByRole("radio", { name: /GitHub app/ });
		const open = within(form).getByRole("radio", { name: /Public repository/ });
		expect(token.closest("label")).toHaveTextContent(
			/paste a fine-grained token with read access to the repository/i,
		);
		expect(app.closest("label")).toHaveTextContent(
			/install it on the organization; no token to manage, syncs on every push/i,
		);
		expect(open.closest("label")).toHaveTextContent(/no credential needed/i);
		expect(form.textContent).not.toMatch(/recommended/i);
	});

	it("asks only for what the chosen way to connect needs", async () => {
		renderPanel(<DatasourcesPanel client={client()} orgId="org-1" />);
		const form = await openGitHubForm();
		fireEvent.click(within(form).getByRole("radio", { name: /Access token/ }));
		expect(within(form).getByLabelText("Access token")).toHaveAttribute(
			"type",
			"password",
		);
		expect(
			within(form).queryByRole("button", { name: /Install the GitHub app/ }),
		).toBeNull();

		fireEvent.click(within(form).getByRole("radio", { name: /GitHub app/ }));
		await waitFor(() =>
			expect(within(form).queryByLabelText("Access token")).toBeNull(),
		);
		expect(
			within(form).getByRole("button", { name: /Install the GitHub app/ }),
		).toBeVisible();

		fireEvent.click(
			within(form).getByRole("radio", { name: /Public repository/ }),
		);
		await waitFor(() =>
			expect(
				within(form).queryByRole("button", { name: /Install the GitHub app/ }),
			).toBeNull(),
		);
		expect(within(form).queryByLabelText("Access token")).toBeNull();
	});

	it("never keeps a token in the page once it is saved", async () => {
		const datasources = client();
		renderPanel(<DatasourcesPanel client={datasources} orgId="org-1" />);
		const form = await openGitHubForm();
		fireEvent.click(within(form).getByRole("radio", { name: /Access token/ }));
		fireEvent.change(within(form).getByLabelText("Repository"), {
			target: { value: "acme/handbook-two" },
		});
		fireEvent.change(within(form).getByLabelText("New collection name"), {
			target: { value: "Handbook two" },
		});
		fireEvent.change(within(form).getByLabelText("Access token"), {
			target: { value: "github_pat_SECRET123" },
		});
		fireEvent.click(
			within(form).getByRole("button", { name: "Connect repository" }),
		);
		await waitFor(() =>
			expect(datasources.addGitHubSource).toHaveBeenCalledWith(
				expect.objectContaining({
					repo: "acme/handbook-two",
					accessToken: "github_pat_SECRET123",
				}),
			),
		);
		expect(document.body.innerHTML).not.toContain("github_pat_SECRET123");
		for (const input of document.querySelectorAll("input"))
			expect(input.value).not.toBe("github_pat_SECRET123");
	});

	it("says a token is saved, and offers to replace or remove it without ever showing it", async () => {
		const confirm = vi.fn(() => true);
		vi.stubGlobal("confirm", confirm);
		const datasources = client({
			listSources: vi.fn(async () => [
				{ ...handbook, credentialMode: "token" as const },
			]),
			reconnectSource: vi.fn(async () => "job"),
			removeSourceCredential: vi.fn(async () => "job"),
		});
		renderPanel(<DatasourcesPanel client={datasources} orgId="org-1" />);
		const row = await sourceRow("acme/handbook");
		expect(row).toHaveTextContent("Token saved");

		fireEvent.click(
			within(row).getByRole("button", { name: "More for acme/handbook" }),
		);
		expect(
			await screen.findByRole("menuitem", { name: "Replace token" }),
		).toBeVisible();
		fireEvent.click(screen.getByRole("menuitem", { name: "Remove token" }));
		expect(confirm).toHaveBeenCalledOnce();
		await waitFor(() =>
			expect(datasources.removeSourceCredential).toHaveBeenCalledWith(
				"org-1",
				SOURCE,
			),
		);
		expect(await screen.findByText(/no longer uses a token/)).toBeVisible();
		vi.unstubAllGlobals();
	});

	it("offers no token to replace or remove for a source connected through the app", async () => {
		renderPanel(
			<DatasourcesPanel
				client={client({
					listSources: vi.fn(async () => [
						{ ...handbook, credentialMode: "app" as const },
					]),
					reconnectSource: vi.fn(async () => "job"),
					removeSourceCredential: vi.fn(async () => "job"),
				})}
				orgId="org-1"
			/>,
		);
		const row = await sourceRow("acme/handbook");
		expect(row).toHaveTextContent("GitHub app");
		expect(row).not.toHaveTextContent("Token saved");
		expect(
			within(row).queryByRole("button", { name: "More for acme/handbook" }),
		).toBeNull();
	});

	it("shows no identifier and no word about how the host works", async () => {
		renderPanel(<DatasourcesPanel client={client()} orgId="org-1" />);
		const row = await sourceRow("acme/handbook");
		await waitFor(() => expect(row).toHaveTextContent("Engineering"));
		fireEvent.click(
			within(row).getByRole("button", { name: "Sync now acme/handbook" }),
		);
		await screen.findByText(/is syncing/);
		await openGitHubForm();
		const text = document.body.textContent ?? "";
		expect(text).not.toMatch(UUID);
		expect(text).not.toMatch(INTERNAL);
		expect(text).not.toContain("9f8e7d6");
	});
});
