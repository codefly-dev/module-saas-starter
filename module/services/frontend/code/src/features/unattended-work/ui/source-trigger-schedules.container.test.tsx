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
	// The host still fires the schedule itself, and NOT on the declared cron's
	// instant: the sweep reads the source's stored instant, which every pass
	// moves to `now + interval`. The two share a period and not a phase, which
	// is why the fixture's two instants differ by more than a label.
	hostSweepEnabled: true,
	hostSweepDueAt: "2026-10-12T09:07:00Z",
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
	await within(table).findByText(/Declared · next occurrence/);
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

// THE DECLARED OCCURRENCE AND THE PASS THAT WILL HAPPEN ARE TWO FACTS
// (issue #1121, N6).
//
// The panel used to show the cron's instant alone and claim the stopgap agreed
// with it. It does not: the cron comes due on the half hour while the sweep
// fires on the stored instant, so a reader was shown 09:30 for a pass that
// happens at 09:07. Both rows are here, and the host's row says what it is.
describe("the host's own sweep", () => {
	it("shows the instant it will sweep on, beside the declared occurrence", async () => {
		serve({ declaration: declared, delegations: [], children: [] });
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		const table = await admissions();

		const declaredRow = within(table).getByText(/Declared · next occurrence/);
		const sweepRow = within(table).getByText(
			/This host’s stopgap sweep · next pass/,
		);
		expect(declaredRow).toBeTruthy();
		expect(sweepRow).toBeTruthy();
		// The two instants are different, which is the whole finding. Compared
		// as rendered rather than as ISO strings, because the cells are local
		// time.
		const declaredAt = new Date("2026-10-12T09:30:00Z").toLocaleString();
		const sweepAt = new Date("2026-10-12T09:07:00Z").toLocaleString();
		expect(declaredAt).not.toBe(sweepAt);
		expect(
			table.querySelector('[data-slot="next-occurrence"]')?.textContent,
		).toBe(declaredAt);
		expect(
			table.querySelector('[data-slot="host-sweep-due"]')?.textContent,
		).toBe(sweepAt);
		// The sweep admits no operation, so it has no effect identity to show —
		// said rather than left as a dash that would read as "it has none".
		expect(
			within(table).getByText(/None — the sweep admits no operation/),
		).toBeTruthy();
	});

	it("says the host takes no pass when an operator turned the sweep off", async () => {
		// The switch exists so the declaration's consumer and the stopgap can
		// never both run. With it off there is no host pass at all, and showing
		// an instant would be describing one.
		serve({
			declaration: {
				...declared,
				hostSweepEnabled: false,
				hostSweepDueAt: undefined,
			},
			delegations: [],
			children: [],
		});
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		const table = await admissions();

		expect(
			within(table).getByText(/This host’s stopgap sweep · off/),
		).toBeTruthy();
		// NOT "an executing module fires the declaration", which asserted that
		// something else does fire it — a readiness claim this host cannot make
		// (issue #1121, F1). The switch says only that this host stopped.
		expect(within(table).getByText(/This host does not fire it/)).toBeTruthy();
		expect(
			within(table).queryByText(/An executing module fires the declaration/),
		).toBeNull();
		expect(
			table.querySelector('[data-slot="host-sweep-due"]')?.textContent,
		).toBe("—");
		// The declaration itself is unchanged by the switch: it is what the
		// source declares, not what this host does about it.
		expect(
			table.querySelector('[data-slot="next-occurrence"]')?.textContent,
		).toBe(new Date("2026-10-12T09:30:00Z").toLocaleString());
	});
});

// TWO CLAIMS THE PANEL MADE THAT THIS HOST CANNOT BACK (issue #1121, the
// final-amendment review).
//
// Both are about the host-sweep row, and both are render-side: the backend
// already carries every fact either one needs.
describe("what the panel says about this host's own sweep", () => {
	// The sweep does not care whether the source DECLARES a trigger. Its due
	// query selects active sources whose stored instant has elapsed, and
	// `DeclareSourceTrigger` computes `hostSweepDueAt` before the cron check
	// precisely so this case is reportable — `TestAnUndeclarableSourceStillReportsTheHostSweep`
	// pins that in pkg/adapters. So a source whose interval no five-field cron
	// can express is swept every interval while the panel showed "declares no
	// trigger" and nothing else: the operator was told the host does nothing
	// for a source it reconciles on a timer.
	it("reports the sweep for a source that declares no trigger", async () => {
		serve({
			declaration: {
				release: "saas_datasource_reconcile",
				operation: "/saas.accounts.v1.DatasourceService/ReconcileSource",
				undeclarableReason:
					"the reconcile interval is not one a five-field cron expression can express",
				hostSweepEnabled: true,
				hostSweepDueAt: "2026-10-12T09:07:00Z",
			},
			delegations: [],
			children: [],
		});
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText(/not one a five-field cron expression can express/);
		const table = await waitFor(() => {
			const found = document.querySelector<HTMLElement>(
				'[data-slot="admissions"]',
			);
			expect(found).not.toBeNull();
			return found as HTMLElement;
		});

		await waitFor(() =>
			expect(table.querySelector('[data-slot="host-sweep"]')).not.toBeNull(),
		);
		expect(
			table.querySelector('[data-slot="host-sweep-due"]')?.textContent,
		).toBe(new Date("2026-10-12T09:07:00Z").toLocaleString());
		// And the DECLARATION's own rows stay withheld, which was the half the
		// old conditional had right: without a schedule there is no declared
		// occurrence to show.
		expect(
			table.querySelectorAll('[data-slot="scheduled-occurrence"]'),
		).toHaveLength(0);
	});

	// With the sweep off, "an executing module fires the declaration" asserts
	// that something else does fire it. Nothing this host holds verifies that
	// (issue #1121, F1): the switch says only that THIS host stopped, so a
	// deployment that turned it off before a runtime consumed the declaration
	// read as covered while nothing fired at all.
	it("does not claim an executing module fires it when the sweep is off", async () => {
		serve({
			declaration: {
				...declared,
				identityHolder: "module-a",
				hostSweepEnabled: false,
				hostSweepDueAt: undefined,
			},
			delegations: [],
			children: [],
		});
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		const table = await admissions();

		const sweep = table.querySelector<HTMLElement>('[data-slot="host-sweep"]');
		expect(sweep).not.toBeNull();
		expect((sweep as HTMLElement).textContent).not.toMatch(
			/An executing module fires the declaration/,
		);
		expect((sweep as HTMLElement).textContent).toMatch(/unverified/);
		// The holder is reported in the declared-identity row, so the two facts
		// are not conflated into one claim.
		expect(screen.getByText(/held by module-a/)).toBeTruthy();
	});

	// A declaration that has not arrived — a read still in flight, or one that
	// failed — must not render a sweep row at all: "off" would be an answer
	// about this host that nothing established.
	it("says nothing about the sweep before the declaration lands", async () => {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				HttpResponse.json({}, { status: 500 }),
			),
		);
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("acme/handbook");

		await waitFor(() =>
			expect(document.querySelector('[data-slot="admissions"]')).not.toBeNull(),
		);
		expect(document.querySelector('[data-slot="host-sweep"]')).toBeNull();
		expect(
			document.querySelector('[data-slot="scheduled-occurrence"]'),
		).toBeNull();
	});
});

// WHAT ONE SOURCE'S ROW SAYS WHEN THE DECLARATION READ DID NOT SUCCEED
// (issue #1121, the final UI review).
//
// The list read is classified; the PER-SOURCE declaration read was not, and a
// row that reads only `data` answers three different situations the same way.
// All three probes drive the real hooks through the container harness.
describe("a per-source declaration read that is refused, pending or stale", () => {
	// Cold refusal: the host answered nothing, and the row said "None —
	// occurrences would be refused", which is a definite statement about this
	// source's identity. `declaredIdentity(undefined)` treated unknown as
	// absent, so a viewer whose read was denied was told the trigger has no
	// identity rather than that they cannot see it.
	it("does not report an absent identity when the read was refused", async () => {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				HttpResponse.json(
					{ code: "permission_denied", message: "denied" },
					{ status: 403 },
				),
			),
		);
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("acme/handbook");

		await waitFor(() =>
			expect(
				document.querySelector('[data-slot="source-trigger"]'),
			).not.toBeNull(),
		);
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);
		expect(
			screen.queryByText("None — occurrences would be refused"),
		).toBeNull();
		// And nothing about this host's own sweep, which the read never answered.
		expect(document.querySelector('[data-slot="host-sweep"]')).toBeNull();
	});

	// A refusal AFTER a success must not leave the previous answer on screen:
	// TanStack keeps the last good data when a refetch rejects, so the row went
	// on showing a cached declaration — including the sweep row this amendment
	// added — to a viewer who is no longer allowed to see it. The clearing
	// belongs in the query function, where the refusal is classified.
	it("drops a cached declaration when a later read is refused", async () => {
		let denied = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				denied
					? HttpResponse.json(
							{ code: "permission_denied", message: "denied" },
							{ status: 403 },
						)
					: HttpResponse.json({
							declaration: {
								release: "saas_datasource_reconcile",
								operation:
									"/saas.accounts.v1.DatasourceService/ReconcileSource",
								undeclarableReason: "the source has no reconcile schedule",
								hostSweepEnabled: true,
								hostSweepDueAt: "2026-10-12T09:07:00Z",
							},
							delegations: [],
							children: [],
						}),
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("the source has no reconcile schedule");
		await waitFor(() =>
			expect(document.querySelector('[data-slot="host-sweep"]')).not.toBeNull(),
		);

		denied = true;
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});

		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);
		expect(
			screen.queryByText("the source has no reconcile schedule"),
		).toBeNull();
		expect(document.querySelector('[data-slot="host-sweep"]')).toBeNull();
	});

	// The sweep is ON and this source has no due instant — a parked source,
	// whose stored instant the park path cleared. "next pass —" reads as a pass
	// whose time is unknown; the supplied facts say there is no pass due.
	it("says the sweep has nothing due rather than a next pass with no time", async () => {
		serve({
			declaration: {
				release: "saas_datasource_reconcile",
				operation: "/saas.accounts.v1.DatasourceService/ReconcileSource",
				undeclarableReason:
					"the source is parked and is not on its reconcile schedule",
				hostSweepEnabled: true,
			},
			delegations: [],
			children: [],
		});
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText(/parked and is not on its reconcile schedule/);
		const sweep = await waitFor(() => {
			const found = document.querySelector<HTMLElement>(
				'[data-slot="host-sweep"]',
			);
			expect(found).not.toBeNull();
			return found as HTMLElement;
		});

		expect(sweep.textContent).not.toMatch(/next pass/);
		expect(sweep.textContent).toMatch(/nothing due/i);
		expect(
			sweep.querySelector('[data-slot="host-sweep-due"]')?.textContent,
		).toBe("—");
	});
});
