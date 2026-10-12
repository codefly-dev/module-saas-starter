import { notifySourceReconcileAdmitted } from "@codefly-dev/saas-ui";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { SourceTriggerSchedules } from "./source-trigger-schedules";

// A PERSON'S PASS AND A SCHEDULE'S OCCURRENCE ARE ONE KIND OF ADMISSION.
//
// That is the claim this panel makes by listing them in one table, and it is
// what issue #1094 made literally true: both admit
// `DatasourceService/ReconcileSource`, both carry an effect identity, and the
// host's receipts cannot tell them apart. So the test is not "a row renders" —
// it is that the manual row sits in the SAME table as the scheduled ones and
// names the same operation.

const SOURCE = "aaaaaaaa-0000-4000-8000-000000000001";

const sources = {
	datasources: [
		{
			id: SOURCE,
			orgId: "org-1",
			provider: "DATASOURCE_PROVIDER_GITHUB",
			github: { repo: "acme/handbook", branch: "main" },
			status: "DATASOURCE_STATUS_ACTIVE",
		},
	],
};

const declared = {
	release: "saas_datasource_reconcile",
	operation: "/saas.accounts.v1.DatasourceService/ReconcileSource",
	schedule: {
		cron: "*/30 * * * *",
		timezone: "UTC",
		missedRun: "skip",
		overlap: "skip",
		deadline: "600000000000",
	},
	identity: { source: SOURCE },
	digest: "4f1c",
	lastOccurrenceAt: "2026-10-12T09:00:00Z",
	nextOccurrenceAt: "2026-10-12T09:30:00Z",
};

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

function serve(declaration: JsonBodyType) {
	server.use(
		http.post(rpc("DatasourceService", "ListSources"), () =>
			HttpResponse.json(sources),
		),
		http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
			HttpResponse.json(declaration),
		),
	);
}

/** The admissions table for the one source, once the declaration has landed. */
async function admissions(): Promise<HTMLElement> {
	await screen.findByText("acme/handbook");
	const table = await waitFor(() => {
		const found = document.querySelector<HTMLElement>(
			'[data-slot="admissions"]',
		);
		expect(found).not.toBeNull();
		return found as HTMLElement;
	});
	await within(table).findByText(/Scheduled · next occurrence/);
	return table;
}

describe("SourceTriggerSchedules", () => {
	it("lists a manual pass in the same table as the scheduled occurrences", async () => {
		serve({ declaration: declared, delegations: [], children: [] });
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		const table = await admissions();

		// Before any press: the two scheduled rows, and the panel says why no
		// manual row is there rather than implying none was ever taken.
		expect(table.querySelectorAll('[data-slot="manual-pass"]')).toHaveLength(0);
		expect(
			screen.getByText(/No manual pass has been admitted from this browser/),
		).toBeTruthy();

		act(() =>
			notifySourceReconcileAdmitted({
				sourceId: SOURCE,
				effectId: "effect-aaaa",
				jobId: "job-1",
				status: "committed",
				repeat: false,
				at: "2026-10-12T09:07:00Z",
			}),
		);

		// The manual row is in the SAME table — not a separate list below it —
		// and carries the effect identity the pass committed under.
		const manual = await waitFor(() => {
			const rows = table.querySelectorAll<HTMLElement>(
				'[data-slot="manual-pass"]',
			);
			expect(rows).toHaveLength(1);
			return rows[0];
		});
		expect(manual.textContent).toContain("Sync now");
		expect(manual.textContent).toContain("effect-aaaa");
		expect(manual.textContent).toContain("job-1");
		expect(
			table.querySelectorAll('[data-slot="scheduled-occurrence"]'),
		).toHaveLength(2);

		// One operation for both kinds of admission, read off the declaration so
		// the panel cannot hold its own opinion of what "the same" means.
		expect(
			screen.getByText("/saas.accounts.v1.DatasourceService/ReconcileSource"),
		).toBeTruthy();
	});

	it("reports a recovered receipt as one admission, not two", async () => {
		serve({ declaration: declared, delegations: [], children: [] });
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		const table = await admissions();

		const pass = {
			sourceId: SOURCE,
			effectId: "effect-bbbb",
			jobId: "job-2",
			status: "committed" as const,
			repeat: false,
			at: "2026-10-12T09:07:00Z",
		};
		act(() => notifySourceReconcileAdmitted(pass));
		act(() =>
			notifySourceReconcileAdmitted({
				...pass,
				repeat: true,
				at: "2026-10-12T09:08:00Z",
			}),
		);

		// The identity is the pass's NAME, so a repeat replaces its row. Two rows
		// under one identity would report one admission as two, which is the
		// confusion this panel exists to remove.
		const manual = await waitFor(() => {
			const rows = table.querySelectorAll<HTMLElement>(
				'[data-slot="manual-pass"]',
			);
			expect(rows).toHaveLength(1);
			return rows[0];
		});
		expect(manual.textContent).toContain("receipt read back");
		expect(manual.textContent).toContain("effect-bbbb");
	});

	it("still lists a manual pass for a source that declares no trigger", async () => {
		serve({
			declaration: {
				release: "saas_datasource_reconcile",
				operation: "/saas.accounts.v1.DatasourceService/ReconcileSource",
				undeclarableReason: "the source has no reconcile schedule",
			},
			delegations: [],
			children: [],
		});
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("the source has no reconcile schedule");

		act(() =>
			notifySourceReconcileAdmitted({
				sourceId: SOURCE,
				effectId: "effect-cccc",
				jobId: "job-3",
				status: "committed",
				repeat: false,
				at: "2026-10-12T09:07:00Z",
			}),
		);

		// The declaration is refused for the INTERVAL it carries, not for the
		// operation, and a manual pass goes through no schedule at all — so the
		// undeclarable reason replaces the scheduled rows, never the table.
		const table = document.querySelector<HTMLElement>(
			'[data-slot="admissions"]',
		);
		expect(table).not.toBeNull();
		await waitFor(() =>
			expect(
				(table as HTMLElement).querySelectorAll('[data-slot="manual-pass"]'),
			).toHaveLength(1),
		);
		expect(
			(table as HTMLElement).querySelectorAll(
				'[data-slot="scheduled-occurrence"]',
			),
		).toHaveLength(0);
	});
});
