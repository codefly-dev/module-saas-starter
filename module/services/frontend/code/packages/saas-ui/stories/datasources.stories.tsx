import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	ConnectGitHubForm,
	DatasourcesPanel,
	type DatasourceClient,
	type DatasourceView,
} from "../src/index.js";

export default { title: "SaaS UI/Datasources" };
const source: DatasourceView = {
	id: "example-source",
	orgId: "example-org",
	provider: "github",
	repo: "example/docs",
	paths: ["docs/"],
	branch: "main",
	boundaryNodeId: "example-boundary",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
	createdAt: undefined,
};
function Preview({
	state,
}: {
	state: "populated" | "empty" | "loading" | "error";
}) {
	const [queryClient] = useState(
		() => new QueryClient({ defaultOptions: { queries: { retry: false } } }),
	);
	const [client] = useState<DatasourceClient>(() => ({
		listSources: async () => {
			if (state === "loading") return new Promise(() => {});
			if (state === "error") throw new Error("Example provider unavailable");
			return state === "empty" ? [] : [source];
		},
		listAccessibleScopes: async () => [
			{
				nodeId: "example-boundary",
				label: "Example documents",
				kind: "collection",
				actions: ["read"],
			},
		],
		addGitHubSource: async () => {
			throw new Error("Connection is unavailable in this preview.");
		},
		syncSource: async () => {
			throw new Error("Synchronization is unavailable in this preview.");
		},
		deleteSource: async () => {
			throw new Error("Deletion is unavailable in this preview.");
		},
	}));
	return (
		<QueryClientProvider client={queryClient}>
			<DatasourcesPanel client={client} orgId="example-org" />
		</QueryClientProvider>
	);
}
export const Populated = { render: () => <Preview state="populated" /> };
export const Empty = { render: () => <Preview state="empty" /> };
export const Loading = { render: () => <Preview state="loading" /> };
export const ProviderError = { render: () => <Preview state="error" /> };
export const ConnectionValidation = {
	render: function ConnectionValidationStory() {
		const [message, setMessage] = useState<string>();
		return (
			<ConnectGitHubForm
				isPending={false}
				errorMessage={message}
				onCancel={() => setMessage("Close this story to leave the preview.")}
				onSubmit={() =>
					setMessage("Connection is unavailable in this preview.")
				}
			/>
		);
	},
};
export const ConnectionPending = {
	render: () => (
		<ConnectGitHubForm
			isPending
			errorMessage="Example pending validation; no request is running."
			onCancel={() => {}}
			onSubmit={() => {}}
		/>
	),
};
