import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import { onSourceReconcileAdmitted } from "../reconcile-passes.js";
import type {
	DatasourceClient,
	DatasourceView,
	SourceReconcileReceipt,
} from "../types.js";

// "SYNC NOW" IS AN ADMISSION OF THE SAME OPERATION A SCHEDULE'S OCCURRENCE IS.
//
// What these cover that the panel's own suite does not: the operation protocol
// underneath the button. A pass carries an effect identity the kit minted, a
// repeat press recovers the FIRST pass's receipt instead of starting a second,
// and the identity survives a failure so that a retry is a recovery rather
// than a second full re-send of the source.

afterEach(cleanup);

const source: DatasourceView = {
	id: "ds-1",
	orgId: "org-1",
	provider: "github",
	repo: "acme/handbook",
	paths: ["docs/"],
	branch: "main",
	boundaryNodeId: "11111111-1111-1111-1111-111111111111",
	webhookConfigured: true,
	status: "active",
	lastSyncedAt: undefined,
	createdAt: undefined,
};

function renderPanel(client: DatasourceClient): void {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<DatasourcesPanel client={client} orgId="org-1" />
		</QueryClientProvider>,
	);
}

/** A client whose reconcile commits, recording the identity each pass used. */
function committingClient(overrides: Partial<DatasourceClient> = {}) {
	const admitted: string[] = [];
	const receipts = new Map<string, SourceReconcileReceipt>();
	const client: DatasourceClient = {
		listSources: vi.fn(async () => [source]),
		addGitHubSource: vi.fn(async () => {}),
		reconcileSource: vi.fn(async (_orgId, _id, effectId: string) => {
			admitted.push(effectId);
			// The host is idempotent on the identity: a second call under one
			// identity returns the receipt the first committed, never a new job.
			const held = receipts.get(effectId);
			if (held) return held;
			const receipt: SourceReconcileReceipt = {
				effectId,
				status: "committed",
				jobId: `job-${receipts.size + 1}`,
			};
			receipts.set(effectId, receipt);
			return receipt;
		}),
		lookupReconcileSource: vi.fn(async (effectId: string) => {
			return receipts.get(effectId);
		}),
		deleteSource: vi.fn(async () => {}),
		...overrides,
	};
	return { client, admitted, receipts };
}

const press = async () =>
	fireEvent.click(await screen.findByRole("button", { name: /^Sync$/ }));

const notice = async () =>
	(await screen.findByRole("status")).textContent ?? "";

const settled = () =>
	waitFor(() =>
		expect(screen.queryByRole("button", { name: /^Syncing/ })).toBeNull(),
	);

it("admits one pass under an effect identity the kit minted", async () => {
	const { client, admitted } = committingClient();
	renderPanel(client);
	await press();
	await settled();
	expect(admitted).toHaveLength(1);
	// Minted here, never taken from the host: an identity the server chose
	// would make every press a new pass.
	expect(admitted[0]).toBeTruthy();
	expect(client.lookupReconcileSource).not.toHaveBeenCalled();
	// The outcome the operation returned, not an acknowledgement that a request
	// was sent: the identity and the job are both on screen.
	const text = await notice();
	expect(text).toContain(admitted[0]);
	expect(text).toContain("job-1");
});

it("answers a double press with the first pass's receipt, not a second pass", async () => {
	const { client, admitted, receipts } = committingClient();
	renderPanel(client);
	await press();
	await settled();
	await press();
	await settled();

	// One identity, one receipt, one job — the whole point of the operation.
	expect(new Set(admitted).size).toBe(1);
	expect(receipts.size).toBe(1);
	// The second press RECOVERED rather than re-admitted: the lookup ran, and
	// the operation was not called a second time.
	expect(client.lookupReconcileSource).toHaveBeenCalledWith(admitted[0]);
	expect(client.reconcileSource).toHaveBeenCalledTimes(1);
	const text = await notice();
	expect(text).toContain("already being reconciled");
	expect(text).toContain(admitted[0]);
	expect(text).toContain("job-1");
});

it("believes the press after a recovered receipt, and it is a new pass", async () => {
	const { client, admitted, receipts } = committingClient();
	renderPanel(client);
	await press();
	await settled();
	await press();
	await settled();
	await press();
	await settled();

	// Two passes, two identities, two jobs — and the middle press produced
	// neither. There is no clock in that rule: the person was told the pass was
	// already recorded, so the next press is a request they mean.
	expect(receipts.size).toBe(2);
	const identities = [...new Set(admitted)];
	expect(identities).toHaveLength(2);
	expect(await notice()).toContain("job-2");
});

it("re-presents the same identity when a lookup is inconclusive", async () => {
	const { client, admitted } = committingClient({
		// The host knows the attempt but holds no receipt: "unknown" is
		// INCONCLUSIVE, never "it did not happen".
		lookupReconcileSource: vi.fn(async (effectId: string) => ({
			effectId,
			status: "unknown" as const,
			jobId: "",
		})),
	});
	renderPanel(client);
	await press();
	await settled();
	await press();
	await settled();

	expect(client.reconcileSource).toHaveBeenCalledTimes(2);
	// The SAME identity both times: a new one would be a second pass over work
	// that may well be running.
	expect(new Set(admitted).size).toBe(1);
});

it("keeps the identity through a failure, so a retry recovers the pass", async () => {
	const receipts = new Map<string, SourceReconcileReceipt>();
	const admitted: string[] = [];
	let failNext = true;
	const client: DatasourceClient = {
		listSources: vi.fn(async () => [source]),
		addGitHubSource: vi.fn(async () => {}),
		reconcileSource: vi.fn(async (_orgId, _id, effectId: string) => {
			admitted.push(effectId);
			if (failNext) {
				failNext = false;
				// The pass may well have committed on the host; the caller cannot
				// tell. That is exactly when the identity must survive.
				throw new Error("gateway timeout");
			}
			const receipt: SourceReconcileReceipt = {
				effectId,
				status: "committed",
				jobId: "job-1",
			};
			receipts.set(effectId, receipt);
			return receipt;
		}),
		lookupReconcileSource: vi.fn(async (effectId: string) =>
			receipts.get(effectId),
		),
		deleteSource: vi.fn(async () => {}),
	};
	renderPanel(client);
	await press();
	await waitFor(() =>
		expect(screen.getByRole("alert").textContent).toContain("gateway timeout"),
	);
	await press();
	await settled();

	expect(admitted).toHaveLength(2);
	expect(admitted[0]).toBe(admitted[1]);
	expect(receipts.size).toBe(1);
});

it("announces each pass with its effect identity, for a host surface to list", async () => {
	const { client, admitted } = committingClient();
	const heard: { effectId: string; sourceId: string; repeat: boolean }[] = [];
	const stop = onSourceReconcileAdmitted((pass) =>
		heard.push({
			effectId: pass.effectId,
			sourceId: pass.sourceId,
			repeat: pass.repeat,
		}),
	);
	const failing = onSourceReconcileAdmitted(() => {
		throw new Error("a listener's failure is its own");
	});
	renderPanel(client);
	await press();
	await settled();
	await press();
	await settled();
	stop();
	failing();

	expect(heard).toEqual([
		{ effectId: admitted[0], sourceId: "ds-1", repeat: false },
		// A repeat is reported under the SAME identity, which is how a listener
		// replaces the row rather than showing one admission as two.
		{ effectId: admitted[0], sourceId: "ds-1", repeat: true },
	]);
});
