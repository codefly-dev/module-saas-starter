import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
 waitFor,
} from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	CollectionReadBoundary,
	CollectionGrants,
} from "../collection-access.js";
import { ConnectGitHubForm } from "../connect-github-form.js";
import type { CollectionAccessView, DatasourceClient } from "../types.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});
const collection: CollectionAccessView = {
	nodeId: "wiki-id",
	label: "Wiki",
	scopePath: "wiki_path",
	grants: [],
};
const client = (overrides: Partial<DatasourceClient>): DatasourceClient => ({
	listSources: async () => [],
	addGitHubSource: async () => {},
	syncSource: async () => "job",
	deleteSource: async () => {},
	...overrides,
});
function PrivateContent() {
	const [message, setMessage] = useState("");
	return (
		<>
			<p>No indexed content yet.</p>
			<input
				aria-label="Chat"
				value={message}
				onChange={(event) => setMessage(event.target.value)}
			/>
		</>
	);
}

describe("collection read access", () => {
	it.each(["revoked", "failed"])(
		"discards mounted private state when permissions are %s",
		async (outcome) => {
			let readable = true;
			const api = client({
				listAccessibleScopes: async () => {
					if (!readable && outcome === "failed") throw new Error("unavailable");
					return readable
						? [
								{
									nodeId: "wiki-id",
									label: "Wiki",
									kind: "collection",
									actions: ["read"],
								},
							]
						: [];
				},
			});
			const cache = new QueryClient({
				defaultOptions: { queries: { retry: false } },
			});
			render(
				<QueryClientProvider client={cache}>
					<CollectionReadBoundary client={api} orgId="org" nodeId="wiki-id">
						<PrivateContent />
					</CollectionReadBoundary>
				</QueryClientProvider>,
			);
			fireEvent.change(await screen.findByLabelText("Chat"), {
				target: { value: "Private conversation" },
			});
			readable = false;
			await act(async () => {
				await cache.invalidateQueries({
					queryKey: ["datasource-boundaries", "org"],
				});
			});
			await waitFor(() => expect(screen.queryByLabelText("Chat")).toBeNull());
			expect(screen.queryByText("No indexed content yet.")).toBeNull();
			expect(
				await screen.findByText(
					outcome === "failed" ? /Couldn’t verify/ : /don’t have read access/,
				),
			).toBeTruthy();
			readable = true;
			await act(async () => {
				await cache.invalidateQueries({
					queryKey: ["datasource-boundaries", "org"],
				});
			});
			expect(
				((await screen.findByLabelText("Chat")) as HTMLInputElement).value,
			).toBe("");
		},
	);
	it("never renders content without a permission client", () => {
		render(
			<QueryClientProvider client={new QueryClient()}>
				<CollectionReadBoundary
					client={client({})}
					orgId="org"
					nodeId="wiki-id"
				>
					<p>Private</p>
				</CollectionReadBoundary>
			</QueryClientProvider>,
		);
		expect(screen.queryByText("Private")).toBeNull();
	});
	it("shows explicit reader and creator policy for the selected collection", () => {
		render(
			<ConnectGitHubForm
				collections={[collection]}
				readableNodeIds={[]}
				onSubmit={vi.fn()}
				onCancel={vi.fn()}
				isPending={false}
			/>,
		);
		fireEvent.change(screen.getByLabelText("Existing collection"), {
			target: { value: "wiki-id" },
		});
		expect(screen.getByText(/Readers: No collection read grants/)).toBeTruthy();
		expect(screen.getByText(/You do not have documents\/read/)).toBeTruthy();
		expect(
			(screen.getByLabelText("Target collection") as HTMLInputElement).readOnly,
		).toBe(true);
	});
	it("grants a chosen team and revokes the exact inherited grant with actor identity", async () => {
		const subject = {
			id: "team",
			kind: "team" as const,
			label: "Example Team",
		};
		const grant = {
			id: "grant",
			subjectId: "team",
			subjectKind: "team" as const,
			scopePath: "root",
			roleId: "reader",
			subjectLabel: "Example Team",
			roleName: "Collection reader",
			actorLabel: "Jane Doe",
		};
		const api = client({
			listGrantSubjects: async () => [subject],
			grantCollectionRead: vi.fn(async () => {}),
			revokeCollectionRead: vi.fn(async () => {}),
		});
		vi.spyOn(window, "confirm").mockReturnValue(true);
		render(
			<QueryClientProvider client={new QueryClient()}>
				<CollectionGrants
					client={api}
					orgId="org"
					collection={{ ...collection, grants: [grant] }}
				/>
			</QueryClientProvider>,
		);
		expect(screen.getByText(/Granted by Jane Doe/)).toBeTruthy();
		await screen.findByRole("option", { name: "Example Team" });
		fireEvent.change(screen.getByLabelText("Grant read access to"), {
			target: { value: "team:team" },
		});
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: "Grant read access" }),
			);
		});
		expect(api.grantCollectionRead).toHaveBeenCalledWith(
			"org",
			"wiki_path",
			subject,
		);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "Revoke grant" }));
		});
		expect(api.revokeCollectionRead).toHaveBeenCalledWith("org", grant);
	});
});

it("observes a grant revoked elsewhere while the collection stays open", async () => {
 vi.useFakeTimers();
 try {
  let readable = true;
  const api = client({listAccessibleScopes: async () => readable ? [{nodeId: "wiki-id", label: "Wiki", kind: "collection", actions: ["read"]}] : []});
  const cache = new QueryClient({defaultOptions: {queries: {retry: false}}});
  render(<QueryClientProvider client={cache}><CollectionReadBoundary client={api} orgId="org" nodeId="wiki-id"><p>Private document</p></CollectionReadBoundary></QueryClientProvider>);
  await act(async () => {await vi.advanceTimersByTimeAsync(10);});
  expect(screen.getByText("Private document")).toBeTruthy();
  readable = false;
  await act(async () => {await vi.advanceTimersByTimeAsync(5010);});
  expect(screen.queryByText("Private document")).toBeNull();
  expect(screen.getByText(/don’t have read access/)).toBeTruthy();
  cleanup();
  cache.clear();
 } finally {vi.useRealTimers();}
});
