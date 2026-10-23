import { DatasourceService } from "@codefly-dev/saas-sdk";
import { createRouterTransport } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
	CatalogProviders,
	RefusedProvider,
} from "../../../stories/datasources.stories.js";
import {
	exampleProvider,
	refusedProvider,
	repositoryProvider,
} from "../../../stories/catalog-fixtures.js";
import { DatasourcesPanel } from "../datasources-panel.js";
import { datasourceClientOverTransport } from "../gateway.js";
import type {
	ConnectSourceInput,
	DatasourceClient,
	DatasourceProviderView,
} from "../types.js";

afterEach(cleanup);
function clientWith(providers: DatasourceProviderView[]): DatasourceClient {
	return {
		listSources: async () => [],
		addGitHubSource: vi.fn(),
		reconcileSource: vi.fn(),
		lookupReconcileSource: vi.fn(),
		deleteSource: vi.fn(),
		getCatalog: vi.fn(async () => providers),
		addSource: vi.fn(async () => {}),
	};
}
function mount(client: DatasourceClient) {
	const queries = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	const view = render(
		<QueryClientProvider client={queries}>
			<DatasourcesPanel client={client} orgId="example-org" />
		</QueryClientProvider>,
	);
	return { ...view, queries };
}
async function fill(provider: DatasourceProviderView) {
	fireEvent.click(
		await screen.findByRole("button", {
			name: `Connect ${provider.displayName}`,
		}),
	);
	fireEvent.change(screen.getByLabelText("Target collection"), {
		target: { value: "Example collection" },
	});
	fireEvent.change(screen.getByLabelText(provider.fields[0].displayName), {
		target: { value: "example/" },
	});
	fireEvent.change(screen.getByLabelText("Credential"), {
		target: { value: "example-secret" },
	});
}

it("offers every admitted descriptor, including one unknown to this kit, and passes its own fields", async () => {
	const client = clientWith([
		repositoryProvider,
		exampleProvider,
		refusedProvider,
	]);
	mount(client);
	for (const provider of [repositoryProvider, exampleProvider]) {
		await fill(provider);
		if (provider === exampleProvider) {
			fireEvent.change(screen.getByLabelText("Include patterns"), {
				target: { value: "docs/**\nnotes/**" },
			});
			fireEvent.change(screen.getByLabelText("Exclude patterns"), {
				target: { value: "**/private/**" },
			});
			expect(screen.queryByLabelText("Repository")).toBeNull();
		}
		fireEvent.click(screen.getByRole("button", { name: "Connect source" }));
		await screen.findByRole("button", {
			name: `Connect ${provider.displayName}`,
		});
	}
	expect(client.addSource).toHaveBeenNthCalledWith(
		2,
		expect.objectContaining({
			provider: exampleProvider,
			config: {
				prefix: "example/",
				include_patterns: "docs/**\nnotes/**",
				exclude_patterns: "**/private/**",
			},
			credential: {
				kind: "static_secret",
				secret: "example-secret",
				webhookSecret: "",
			},
		}),
	);
	expect(client.addGitHubSource).not.toHaveBeenCalled();
	expect(screen.getByText(refusedProvider.gap).textContent).toBe(
		refusedProvider.gap,
	);
	expect(
		screen.queryByRole("button", {
			name: `Connect ${refusedProvider.displayName}`,
		}),
	).toBeNull();
});

it.each(["removed", "refused", "failed"])(
	"withdraws an open form when a catalog refresh is %s",
	async (change) => {
		const client = clientWith([exampleProvider]);
		const { queries } = mount(client);
		await fill(exampleProvider);
		vi.mocked(client.getCatalog!).mockImplementation(async () => {
			if (change === "failed") throw new Error("example outage");
			return change === "removed"
				? []
				: [
						{
							...exampleProvider,
							acceptsNewSources: false,
							gap: refusedProvider.gap,
						},
					];
		});
		await act(() =>
			queries.invalidateQueries({ queryKey: ["datasource-catalog"] }),
		);
		await waitFor(() =>
			expect(screen.queryByLabelText("Credential")).toBeNull(),
		);
		expect(
			screen.queryByRole("button", {
				name: `Connect ${exampleProvider.displayName}`,
			}),
		).toBeNull();
		expect(client.addSource).not.toHaveBeenCalled();
		if (change === "refused")
			expect(screen.getByText(refusedProvider.gap)).toBeTruthy();
		if (change === "failed")
			expect(screen.getByRole("alert").textContent).toContain(
				"Could not load the provider catalog",
			);
	},
);

it.each([false, true])(
	"clears secrets before a pending call and never caches or echoes them (reject=%s)",
	async (reject) => {
		const client = clientWith([exampleProvider]);
		let finish!: () => void;
		client.addSource = vi.fn(
			() =>
				new Promise<void>((resolve, fail) => {
					finish = () =>
						reject ? fail(new Error("example-secret")) : resolve();
				}),
		);
		const { queries } = mount(client);
		await fill(exampleProvider);
		const input = screen.getByLabelText("Credential") as HTMLInputElement;
		const form = screen.getByRole("form", {
			name: `Connect ${exampleProvider.displayName}`,
		});
		fireEvent.submit(form);
		fireEvent.submit(form);
		expect(client.addSource).toHaveBeenCalledTimes(1);
		expect(input.value).toBe("");
		expect(queries.getMutationCache().getAll()).toHaveLength(0);
		await act(async () => finish());
		expect(
			JSON.stringify(
				queries
					.getQueryCache()
					.getAll()
					.map((query) => query.state),
			),
		).not.toContain("example-secret");
		expect(document.body.textContent).not.toContain("example-secret");
		if (reject)
			expect(
				(screen.getByLabelText("Credential") as HTMLInputElement).value,
			).toBe("");
	},
);

it("discards a credential on cancellation and provider change", async () => {
	mount(clientWith([exampleProvider, repositoryProvider]));
	await fill(exampleProvider);
	fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
	fireEvent.click(
		screen.getByRole("button", {
			name: `Connect ${exampleProvider.displayName}`,
		}),
	);
	expect(screen.getByLabelText("Credential")).toHaveProperty("value", "");
	fireEvent.change(screen.getByLabelText("Credential"), {
		target: { value: "example-secret" },
	});
	fireEvent.click(
		screen.getByRole("button", {
			name: `Connect ${repositoryProvider.displayName}`,
		}),
	);
	expect(screen.getByLabelText("Credential")).toHaveProperty("value", "");
});

it("keeps today's client and props valid without inventing a provider when catalog support is absent", async () => {
	const legacy = clientWith([]);
	delete legacy.getCatalog;
	delete legacy.addSource;
	mount(legacy);
	expect(
		await screen.findByText("Provider catalog unavailable with this client."),
	).toBeTruthy();
	expect(screen.queryByRole("button", { name: /^Connect/ })).toBeNull();
});

it("reads the host catalog over the existing transport and encodes two different AddSource oneofs", async () => {
	const received: unknown[] = [];
	const transport = createRouterTransport((router) =>
		router.service(DatasourceService, {
			getDatasourceCatalog: () => ({
				providers: [
					{
						provider: 1,
						connector: "github",
						displayName: "Example repository",
						acceptsNewSources: true,
						credentialModes: [4],
						configFields: [
							{ key: "repo", displayName: "Repository", required: true },
							{ key: "paths", displayName: "Paths" },
						],
					},
					{
						provider: 4,
						connector: "upload",
						displayName: "Example bucket",
						acceptsNewSources: true,
						credentialModes: [4],
						configFields: [
							{ key: "bucket", displayName: "Bucket", required: true },
							{ key: "prefix", displayName: "Prefix" },
							{ key: "max_objects", displayName: "Limit" },
						],
					},
					{
						provider: 3,
						connector: "crawler",
						displayName: "Example refused",
						conformanceGap: refusedProvider.gap,
					},
				],
			}),
			addSource: (input) => {
				received.push(input);
				return {};
			},
		}),
	);
	const client = datasourceClientOverTransport(transport);
	const catalog = await client.getCatalog!();
	expect(catalog[2].gap).toBe(refusedProvider.gap);
	const connect = (
		provider: DatasourceProviderView,
		config: ConnectSourceInput["config"],
	) =>
		client.addSource!({
			orgId: "example-org",
			provider,
			config,
			targetCollection: "Example",
			credential: { kind: "static_secret", secret: "example-secret" },
		});
	await connect(catalog[0], { repo: "acme/example", paths: "docs/\nnotes/" });
	await connect(catalog[1], {
		bucket: "example-bucket",
		prefix: "docs/",
		max_objects: "12",
	});
	expect(received).toMatchObject([
		{
			provider: 1,
			config: {
				case: "github",
				value: { repo: "acme/example", paths: ["docs/", "notes/"] },
			},
			credential: "example-secret",
		},
		{
			provider: 4,
			config: {
				case: "upload",
				value: { bucket: "example-bucket", prefix: "docs/", maxObjects: 12 },
			},
			credential: "example-secret",
		},
	]);
	await expect(connect(catalog[2], {})).rejects.toThrow("does not accept");
	await expect(
		connect(catalog[1], { bucket: "example", max_objects: "not-json" }),
	).rejects.toThrow("valid JSON");
	expect(received).toHaveLength(2);
});

it.each([CatalogProviders, RefusedProvider])(
	"renders the admitting and refusing owner previews",
	async (story) => {
		const Story = story.render;
		render(<Story />);
		expect(await screen.findByText(refusedProvider.gap)).toBeTruthy();
		const region = screen.getByRole("region", { name: "Source providers" });
		if (story === CatalogProviders)
			expect(
				within(region).getByRole("button", {
					name: `Connect ${exampleProvider.displayName}`,
				}),
			).toBeTruthy();
		else
			expect(
				within(region).queryByRole("button", { name: /^Connect/ }),
			).toBeNull();
	},
);
