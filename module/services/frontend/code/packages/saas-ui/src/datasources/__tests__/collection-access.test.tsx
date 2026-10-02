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
	nodeId: "collection-id",
	label: "Example Collection",
	scopePath: "example_path",
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
									nodeId: "collection-id",
									label: "Example Collection",
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
					<CollectionReadBoundary client={api} orgId="org" nodeId="collection-id">
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
					nodeId="collection-id"
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
			target: { value: "collection-id" },
		});
		expect(screen.getByText(/Readers: No collection read grants/)).toBeTruthy();
		expect(screen.getByText(/You do not have read access/)).toBeTruthy();
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
		selectSubjects("team:team");
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: "Grant read access" }),
			);
		});
		expect(api.grantCollectionRead).toHaveBeenCalledWith(
			"org",
			"example_path",
			subject,
		);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "Revoke grant" }));
		});
		expect(api.revokeCollectionRead).toHaveBeenCalledWith("org", grant);
	});

	// Bulk is the half of this flow that made it unreasonable: an administrator
	// who had just connected a repository for a team of eight repeated the same
	// four steps eight times. One press now grants everyone who was picked.
	it("grants every selected subject in one press", async () => {
		const subjects = [
			{ id: "team", kind: "team" as const, label: "Example Team" },
			{ id: "u1", kind: "principal" as const, label: "Jane Doe" },
			{ id: "u2", kind: "principal" as const, label: "John Roe" },
		];
		const api = client({
			listGrantSubjects: async () => subjects,
			grantCollectionRead: vi.fn(async () => {}),
		});
		render(
			<QueryClientProvider client={new QueryClient()}>
				<CollectionGrants client={api} orgId="org" collection={collection} />
			</QueryClientProvider>,
		);
		await screen.findByRole("option", { name: "Example Team" });
		selectSubjects("team:team", "principal:u1");

		expect(
			screen.getByRole("button", { name: /Grant read access to 2 subjects/ }),
		).toBeTruthy();
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: /Grant read access/ }),
			);
		});

		expect(api.grantCollectionRead).toHaveBeenCalledTimes(2);
		expect(api.grantCollectionRead).toHaveBeenCalledWith(
			"org",
			"example_path",
			subjects[0],
		);
		expect(api.grantCollectionRead).toHaveBeenCalledWith(
			"org",
			"example_path",
			subjects[1],
		);
		// Not the one that was never picked.
		expect(api.grantCollectionRead).not.toHaveBeenCalledWith(
			"org",
			"example_path",
			subjects[2],
		);
		expect(screen.getByText(/Granted read access to/)).toBeTruthy();
	});

	// The loop has no single answer: one subject may be refused while the rest
	// land. Reporting only the first failure would claim the others never
	// happened; reporting only "failed" would send an administrator to re-grant
	// subjects that already hold the role.
	it("reports each subject the grant did not reach, and keeps only those selected", async () => {
		const subjects = [
			{ id: "team", kind: "team" as const, label: "Example Team" },
			{ id: "u1", kind: "principal" as const, label: "Jane Doe" },
		];
		const api = client({
			listGrantSubjects: async () => subjects,
			grantCollectionRead: vi.fn(async (_org, _path, subject) => {
				if (subject.id === "u1") throw new Error("role is not assignable");
			}),
		});
		render(
			<QueryClientProvider client={new QueryClient()}>
				<CollectionGrants client={api} orgId="org" collection={collection} />
			</QueryClientProvider>,
		);
		await screen.findByRole("option", { name: "Example Team" });
		selectSubjects("team:team", "principal:u1");
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: /Grant read access/ }),
			);
		});

		expect(screen.getByText(/Granted read access to Example Team/)).toBeTruthy();
		expect(
			screen.getByText(/Couldn’t grant read access to Jane Doe/),
		).toBeTruthy();
		// The retry is the same press: the one that landed is dropped from the
		// selection, the one that did not stays in it. Read through what pressing
		// Grant again actually does, rather than through the DOM's
		// selectedOptions — this test set those imperatively, so React's
		// controlled value no longer agrees with them.
		(api.grantCollectionRead as ReturnType<typeof vi.fn>).mockClear();
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: /Grant read access/ }),
			);
		});
		expect(api.grantCollectionRead).toHaveBeenCalledTimes(1);
		expect(api.grantCollectionRead).toHaveBeenCalledWith(
			"org",
			"example_path",
			subjects[1],
		);
	});

	// Offering the grant at connect time is the other half: the person is already
	// in the flow and knows what they just connected, rather than being sent on a
	// separate later journey through Manage read grants.
	it("names the repository just connected, and can be put away", async () => {
		const dismiss = vi.fn();
		const api = client({ listGrantSubjects: async () => [] });
		render(
			<QueryClientProvider client={new QueryClient()}>
				<CollectionGrants
					client={api}
					orgId="org"
					collection={collection}
					connectedRepo="acme/handbook"
					onDismiss={dismiss}
				/>
			</QueryClientProvider>,
		);
		expect(
			screen.getByText(/acme\/handbook is connected and syncing/),
		).toBeTruthy();
		expect(screen.getByText(/Nobody can read it yet/)).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Not now" }));
		expect(dismiss).toHaveBeenCalled();
	});

	// An organization with nobody to grant to says so. Before the picker has
	// answered it says nothing at all: an outstanding read must not render as
	// "there is nobody here".
	it("distinguishes an empty roster from a read still in flight", async () => {
		const api = client({ listGrantSubjects: async () => [] });
		render(
			<QueryClientProvider client={new QueryClient()}>
				<CollectionGrants client={api} orgId="org" collection={collection} />
			</QueryClientProvider>,
		);
		expect(screen.queryByText(/has no members or teams/)).toBeNull();
		expect(
			await screen.findByText(/has no members or teams to grant read access/),
		).toBeTruthy();
	});
});

// jsdom does not apply a value assignment to a <select multiple>, so the
// selection is made the way a person makes it: by marking the options.
function selectSubjects(...values: string[]) {
	const picker = screen.getByLabelText(
		"Grant read access to",
	) as HTMLSelectElement;
	for (const option of Array.from(picker.options)) {
		option.selected = values.includes(option.value);
	}
	fireEvent.change(picker);
}

it("observes a grant revoked elsewhere while the collection stays open", async () => {
 vi.useFakeTimers();
 try {
  let readable = true;
  const api = client({listAccessibleScopes: async () => readable ? [{nodeId: "collection-id", label: "Example Collection", kind: "collection", actions: ["read"]}] : []});
  const cache = new QueryClient({defaultOptions: {queries: {retry: false}}});
  render(<QueryClientProvider client={cache}><CollectionReadBoundary client={api} orgId="org" nodeId="collection-id"><p>Private document</p></CollectionReadBoundary></QueryClientProvider>);
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
