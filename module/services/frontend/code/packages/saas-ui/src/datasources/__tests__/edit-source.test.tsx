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
		// The scope the dialog was seeded from, sent back so the host can refuse
		// the save if the source no longer holds it. Without it a form left open
		// across someone else's change would silently restore the scope it was
		// showing.
		expected: { branch: "main", paths: ["docs/"], fileExtensions: [".md"] },
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

// The one state where a reconfigured sync must NOT claim a re-snapshot: the
// host compiled no change set, which a forced reconcile reaches when it parked
// the source instead of snapshotting it — what happens when a narrowed scope is
// still over the ingest cap. Narrowing paths is the documented recovery for
// exactly that, so this is the reader who most needs to know nothing was handed
// off, and substituting the scope note for that sentence told them the opposite.
it("does not claim a re-snapshot on a reconfigured sync that handed nothing off", () => {
	const unchanged: SourceSyncView = {
		jobId: "job-resnapshot",
		phase: "done",
		trigger: "reconfigured",
		queuedAt: "2026-10-20T10:00:00.000Z",
		finishedAt: "2026-10-20T10:00:30.000Z",
		attempt: 1,
		maxAttempts: 24,
	};
	const report = describeSync(unchanged);
	expect(report.state).toBe("unchanged");
	expect(report.detail).toContain("nothing was handed off");
	expect(report.detail).not.toContain("re-snapshots it");

	// An ordinary sync that handed nothing off still reads as it always did.
	expect(describeSync({ ...unchanged, trigger: "manual" }).detail).toBe(
		"The source had not changed, so nothing was handed off.",
	);
});

// Saving a scope the source no longer holds is refused by the host, and the
// dialog has to survive it: the message tells the person to reopen the form,
// which is only useful if the row behind it has been refreshed first.
it("keeps the dialog open and refreshes the list when the scope moved", async () => {
	const listSources = vi.fn(async () => [source]);
	const client = fakeClient({
		listSources,
		updateSource: vi.fn(async () => {
			throw new Error("This source's branch or filters changed after this form was opened");
		}),
	});
	await openEditDialog(client);
	fireEvent.change(screen.getByLabelText("Branch (optional)"), {
		target: { value: "release" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

	const alert = await screen.findByText(/changed after this form was opened/);
	expect(alert).toBeDefined();
	// Still open, still holding what was typed, so nothing has to be retyped.
	expect(fieldValue("Branch (optional)")).toBe("release");
	// Refetched, so reopening the dialog seeds from the current row rather than
	// re-sending the same stale expectation and being refused again.
	await waitFor(() => expect(listSources.mock.calls.length).toBeGreaterThan(1));
});
