import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import { describeSync } from "../sync-progress-model.js";
import type {
	DatasourceClient,
	DatasourceView,
	SourceSyncView,
} from "../types.js";

afterEach(cleanup);

function renderWithClient(ui: ReactElement) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
	);
}

const source: DatasourceView = {
	id: "ds-1",
	orgId: "org-1",
	provider: "github",
	repo: "acme/handbook",
	paths: ["docs/"],
	fileExtensions: [".md"],
	branch: "main",
	boundaryNodeId: "11111111-1111-1111-1111-111111111111",
	boundaryLabel: "Handbook",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
	createdAt: undefined,
};

function fakeClient(
	overrides: Partial<DatasourceClient> = {},
): DatasourceClient {
	return {
		listSources: vi.fn(async () => [source]),
		addGitHubSource: vi.fn(async () => {}),
		reconcileSource: vi.fn(async (_orgId, _id, effectId: string) => ({
			effectId,
			status: "committed" as const,
			jobId: "job-1",
		})),
		lookupReconcileSource: vi.fn(async () => undefined),
		deleteSource: vi.fn(async () => {}),
		updateSource: vi.fn(async () => "job-resnapshot"),
		...overrides,
	};
}

function fieldValue(label: string): string {
	return (
		screen.getByLabelText(label) as HTMLInputElement | HTMLTextAreaElement
	).value;
}

async function openEditDialog(client: DatasourceClient) {
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(
		await screen.findByRole("button", { name: /^More actions for / }),
	);
	fireEvent.click(await screen.findByRole("menuitem", { name: "Edit" }));
}

it("seeds the edit dialog from the source and saves the whole scope", async () => {
	const client = fakeClient();
	const onSyncEnqueued = vi.fn();
	renderWithClient(
		<DatasourcesPanel
			client={client}
			orgId="org-1"
			onSyncEnqueued={onSyncEnqueued}
		/>,
	);
	fireEvent.click(
		await screen.findByRole("button", { name: /^More actions for / }),
	);
	fireEvent.click(await screen.findByRole("menuitem", { name: "Edit" }));

	// The fields arrive holding what the source already fetches, so a person
	// editing one filter does not have to retype the other two — which, with a
	// whole-field replacement, would otherwise clear them.
	expect(fieldValue("Branch (optional)")).toBe("main");
	expect(fieldValue("Paths (optional)")).toBe("docs/");
	expect(fieldValue("File types (optional)")).toBe(".md");

	fireEvent.change(screen.getByLabelText("Branch (optional)"), {
		target: { value: "release" },
	});
	fireEvent.change(screen.getByLabelText("Paths (optional)"), {
		target: { value: "docs/\nguides/" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

	await waitFor(() => expect(client.updateSource).toHaveBeenCalledTimes(1));
	expect(client.updateSource).toHaveBeenCalledWith({
		orgId: "org-1",
		id: "ds-1",
		branch: "release",
		paths: ["docs/", "guides/"],
		fileExtensions: [".md"],
	});
	// Editing a source is not connecting or deleting one: its identity, its
	// credential and its history stay where they are.
	expect(client.addGitHubSource).not.toHaveBeenCalled();
	expect(client.deleteSource).not.toHaveBeenCalled();
	expect((await screen.findByRole("status")).textContent).toContain(
		"re-snapshot is queued",
	);
	expect(onSyncEnqueued).toHaveBeenCalledExactlyOnceWith("job-resnapshot");
});

// An empty job id is the host saying the request changed nothing. Reporting it
// as a queued sync would leave a reader waiting for a pass that never runs.
it("says nothing changed when the host queued no sync", async () => {
	const client = fakeClient({ updateSource: vi.fn(async () => undefined) });
	await openEditDialog(client);
	fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

	await waitFor(() => expect(client.updateSource).toHaveBeenCalledTimes(1));
	expect((await screen.findByRole("status")).textContent).toContain(
		"Nothing changed",
	);
});

// A refused branch is a correctable mistake, so the dialog keeps what was typed
// and shows the host's own sentence.
it("keeps the dialog open and surfaces a refusal", async () => {
	const client = fakeClient({
		updateSource: vi.fn(async () => {
			throw new Error("GitHub repository or branch was not found");
		}),
	});
	await openEditDialog(client);
	fireEvent.change(screen.getByLabelText("Branch (optional)"), {
		target: { value: "gone" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

	expect((await screen.findByRole("alert")).textContent).toContain(
		"was not found",
	);
	expect(fieldValue("Branch (optional)")).toBe("gone");
});

// A client with no updateSource — an older host, or a consumer's own adapter —
// is offered no Edit at all, rather than a dialog whose save cannot be made.
it("offers no Edit to a client that cannot edit a source", async () => {
	const client = fakeClient();
	delete client.updateSource;
	renderWithClient(<DatasourcesPanel client={client} orgId="org-1" />);
	fireEvent.click(
		await screen.findByRole("button", { name: /^More actions for / }),
	);
	await screen.findByRole("menuitem", { name: "Delete" });
	expect(screen.queryByRole("menuitem", { name: "Edit" })).toBeNull();
});

// The repository and the collection are the source's identity: the host refuses
// to move either, so the dialog states them rather than offering them.
it("does not offer the repository or the collection as fields", async () => {
	await openEditDialog(fakeClient());
	expect(screen.queryByLabelText("Repository")).toBeNull();
	expect(screen.queryByLabelText("Target collection")).toBeNull();
	expect(
		screen.getByRole("dialog", { name: "Edit acme/handbook" }).textContent,
	).toContain("its collection Handbook");
});

// The sync a reconfigure starts hands off the whole new scope. Beside an
// ordinary sync's handful of files that reads as a fault unless the progress
// says the source itself moved.
it("states that a reconfigured sync re-snapshots the source", () => {
	const base: SourceSyncView = {
		jobId: "job-resnapshot",
		phase: "done",
		trigger: "reconfigured",
		queuedAt: "2026-10-20T10:00:00.000Z",
		finishedAt: "2026-10-20T10:00:30.000Z",
		changes: {
			files: 412,
			added: 412,
			modified: 0,
			deleted: 0,
			splitKnown: true,
			snapshot: true,
			commit: "abc1234",
		},
		attempt: 1,
		maxAttempts: 24,
	};
	expect(describeSync(base).detail).toContain("re-snapshots it");
	// An ordinary manual sync says nothing extra: the note is a fact about the
	// pass the host reported, not decoration on every snapshot.
	expect(describeSync({ ...base, trigger: "manual" }).detail).toBeUndefined();
});
