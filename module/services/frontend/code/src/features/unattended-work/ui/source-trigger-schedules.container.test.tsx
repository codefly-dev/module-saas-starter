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

// THE REST OF THE CLOSURE CONDITIONS for the per-source declaration read
// (issue #1121, r22 F1): pending, a non-refusal failure, 403 then a transient
// failure, 403 then a remount, recovery on success, and manual admissions
// surviving all of it.
//
// A declaration served only after the test releases it, so "pending" is a real
// state rather than a race.
function heldDeclaration() {
	let release: (() => void) | undefined;
	const gate = new Promise<void>((resolve) => {
		release = resolve;
	});
	server.use(
		http.post(rpc("DatasourceService", "ListSources"), () =>
			HttpResponse.json(sources),
		),
		http.post(
			rpc("DatasourceService", "GetSourceTriggerDeclaration"),
			async () => {
				await gate;
				return HttpResponse.json({
					declaration: declared,
					delegations: [],
					children: [],
				});
			},
		),
	);
	return () => release?.();
}

describe("the declaration read's remaining states", () => {
	it("claims nothing while the read is still in flight", async () => {
		const release = heldDeclaration();
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("acme/handbook");

		// The row exists; its declaration does not yet. Neither an absent
		// identity nor a sweep state may be asserted about a read in flight.
		await waitFor(() =>
			expect(
				document.querySelector('[data-slot="source-trigger"]'),
			).not.toBeNull(),
		);
		expect(
			screen.queryByText("None — occurrences would be refused"),
		).toBeNull();
		expect(document.querySelector('[data-slot="host-sweep"]')).toBeNull();

		release();
		await screen.findByText("*/30 * * * *");
	});

	// A failure that is NOT a refusal reads differently, because the two mean
	// different things: unknown versus not allowed.
	it("separates a failed read from a refused one", async () => {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				HttpResponse.json({ message: "boom" }, { status: 500 }),
			),
		);
		renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("acme/handbook");

		await waitFor(() =>
			expect(screen.getByText(/Couldn.t load/i)).toBeTruthy(),
		);
		expect(screen.queryByText(/don.t have permission/i)).toBeNull();
		expect(
			screen.queryByText("None — occurrences would be refused"),
		).toBeNull();
	});

	// A refusal, then a transient failure. The snapshot the refusal deleted must
	// not come back: a transient failure retains what the cache HAS, and after a
	// refusal it has nothing.
	it("does not resurrect a refused snapshot through a later transient failure", async () => {
		let phase: "ok" | "denied" | "down" = "ok";
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () => {
				if (phase === "ok") {
					return HttpResponse.json({
						declaration: declared,
						delegations: [],
						children: [],
					});
				}
				return phase === "denied"
					? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
					: HttpResponse.json({ message: "boom" }, { status: 500 });
			}),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");

		phase = "denied";
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);

		phase = "down";
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});
		expect(screen.queryByText("*/30 * * * *")).toBeNull();
		expect(document.querySelector('[data-slot="host-sweep"]')).toBeNull();
	});

	// And not through a remount either: the snapshot is gone from the cache, so
	// a component mounting into that same cache has nothing to show.
	it("does not resurrect a refused snapshot through a remount", async () => {
		let denied = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				denied
					? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
					: HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						}),
			),
		);
		const first = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");

		denied = true;
		await act(async () => {
			await first.client.refetchQueries({
				queryKey: ["source-trigger-declaration"],
			});
		});
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);

		cleanup();
		renderInApp(<SourceTriggerSchedules orgId="org-1" />, first.client);
		await screen.findByText("acme/handbook");
		expect(screen.queryByText("*/30 * * * *")).toBeNull();
		expect(document.querySelector('[data-slot="host-sweep"]')).toBeNull();
	});

	// Recovery is exactly one thing: a read that succeeds.
	it("recovers when a later read succeeds", async () => {
		let denied = true;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				denied
					? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
					: HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						}),
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);

		denied = false;
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});
		await screen.findByText("*/30 * * * *");
		await waitFor(() =>
			expect(document.querySelector('[data-slot="host-sweep"]')).not.toBeNull(),
		);
		expect(screen.queryByText(/don.t have permission/i)).toBeNull();
	});

	// A transient failure keeps the last good answer AND says it may be out of
	// date. Blanking a good answer because one request did not come back is
	// worse than the staleness.
	it("keeps a transiently failed answer and labels it stale", async () => {
		let down = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				down
					? HttpResponse.json({ message: "boom" }, { status: 500 })
					: HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						}),
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");

		down = true;
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});

		expect(screen.getByText("*/30 * * * *")).toBeTruthy();
		await waitFor(() =>
			expect(screen.getByText(/may be out of date/i)).toBeTruthy(),
		);
	});

	// Through every one of those, the passes THIS BROWSER admitted stay: they
	// come from the kit's notification, not from the declaration read, and a
	// person's pass goes through no schedule at all.
	it("keeps manual admissions through a refusal", async () => {
		let denied = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				denied
					? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
					: HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						}),
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");
		act(() =>
			notifySourceReconcileAdmitted({
				sourceId: SOURCE,
				effectId: "effect-dddd",
				jobId: "job-9",
				status: "committed",
				repeat: false,
				at: "2026-10-12T09:20:00Z",
			}),
		);
		await screen.findByText("effect-dddd");

		denied = true;
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);

		expect(screen.getByText("effect-dddd")).toBeTruthy();
		expect(document.querySelectorAll('[data-slot="manual-pass"]')).toHaveLength(
			1,
		);
	});
});

// A REFUSAL DELETES ONE DECLARATION, NOT EVERY DECLARATION (issue #1121, the
// parent's scope finding on b3d01c3d3).
//
// `getQueryCache().findAll({ queryKey })` matches by PREFIX in TanStack Query,
// so clearing `["source-trigger-declaration"]` reached every org and every
// source. One source refusing its read then blanked a sibling source whose own
// read had SUCCEEDED — and the damage is invisible at the component, because
// that sibling's query status is still `success` with no error: the row has no
// refusal to report and falls through to "This source declares no trigger",
// which is a false statement about a source the viewer is allowed to see.
//
// The blast radius of a refusal is the key that was refused. These two probes
// pin that at both scopes a key carries: another source in the same
// organization, and the same source in another organization.
const SOURCE_B = "aaaaaaaa-0000-4000-8000-000000000002";

const twoSources = {
	datasources: [
		sources.datasources[0],
		{
			id: SOURCE_B,
			orgId: "org-1",
			provider: "DATASOURCE_PROVIDER_GITHUB",
			github: { repo: "acme/runbooks", branch: "main" },
			status: "DATASOURCE_STATUS_ACTIVE",
		},
	],
};

const declaredB = {
	...declared,
	schedule: { ...declared.schedule, cron: "15 * * * *" },
	identity: { source: SOURCE_B },
	digest: "9e22",
};

describe("one refused declaration does not blank another", () => {
	it("leaves an authorized source's declaration intact when a sibling source is refused", async () => {
		let denyA = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(twoSources),
			),
			http.post(
				rpc("DatasourceService", "GetSourceTriggerDeclaration"),
				async ({ request }) => {
					const body = (await request.json()) as { sourceId?: string };
					if (body.sourceId === SOURCE_B) {
						return HttpResponse.json({
							declaration: declaredB,
							delegations: [],
							children: [],
						});
					}
					return denyA
						? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
						: HttpResponse.json({
								declaration: declared,
								delegations: [],
								children: [],
							});
				},
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");
		await screen.findByText("15 * * * *");

		denyA = true;
		// ONLY A IS REFETCHED, which is the whole point: B's query is not
		// re-run, so if A's refusal deleted B's snapshot nothing restores it.
		// Refetching both would hide the defect, because B's own successful
		// re-read would repopulate what A had just wiped.
		await act(async () => {
			await client.refetchQueries({
				queryKey: ["source-trigger-declaration", "org-1", SOURCE],
				exact: true,
			});
		});

		// A is gone and says why.
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);
		expect(screen.queryByText("*/30 * * * *")).toBeNull();
		// B is UNTOUCHED: its read succeeded, so its declaration, its sweep row
		// and its cached answer all stand. The last assertion is the one that
		// fails on the prefix clear — the component would otherwise claim B
		// declares no trigger while its query sits in `success`.
		expect(screen.getByText("15 * * * *")).toBeTruthy();
		expect(
			client.getQueryData(["source-trigger-declaration", "org-1", SOURCE_B]),
		).toBeTruthy();
		expect(screen.queryByText("This source declares no trigger.")).toBeNull();
	});

	it("leaves another organization's declaration of the same source intact", async () => {
		const other = ["source-trigger-declaration", "org-2", SOURCE] as const;
		let denied = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				denied
					? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
					: HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						}),
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");

		// A second organization's answer for the same source id, which no part
		// of this refusal is about.
		client.setQueryData(other, {
			declaration: declaredB,
			delegations: [],
			children: [],
		});

		denied = true;
		await act(async () => {
			await client.refetchQueries({
				queryKey: ["source-trigger-declaration", "org-1", SOURCE],
			});
		});
		await waitFor(() =>
			expect(screen.getByText(/don.t have permission/i)).toBeTruthy(),
		);

		expect(client.getQueryData(other)).toBeTruthy();
	});
});

// A SUPERSEDED REFUSAL MUST NOT ERASE A NEWER SUCCESS (issue #1121).
//
// Two fetches overlap on one key. The older one is superseded — TanStack
// cancels it when the newer starts — but the request itself is already in
// flight and eventually rejects with 403. Its catch then ran and deleted the
// answer the NEWER fetch had just written, leaving the query in a state no
// component can read correctly: status `success`, fetchStatus `idle`, `data`
// undefined and `error` null. The row has no refusal to report and no
// declaration to show, so it says "This source declares no trigger" about a
// source whose latest read succeeded.
//
// The condition is the one the lifecycle already records: delete only if no
// newer success replaced what this fetch was reading. `dataUpdatedAt` is that
// fact, so the fix reads the field it already writes rather than adding a
// mechanism beside it.
describe("a superseded refusal", () => {
	const KEY = ["source-trigger-declaration", "org-1", SOURCE] as const;

	function overlapping() {
		let call = 0;
		let release403: (() => void) | undefined;
		const held = new Promise<void>((resolve) => {
			release403 = resolve;
		});
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(twoSources),
			),
			http.post(
				rpc("DatasourceService", "GetSourceTriggerDeclaration"),
				async ({ request }) => {
					const body = (await request.json()) as { sourceId?: string };
					if (body.sourceId !== SOURCE) {
						return HttpResponse.json({
							declaration: declaredB,
							delegations: [],
							children: [],
						});
					}
					call++;
					if (call === 1) {
						return HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						});
					}
					if (call === 2) {
						// The OLD fetch: superseded, answers last, and answers 403.
						await held;
						return HttpResponse.json(
							{ code: "permission_denied" },
							{ status: 403 },
						);
					}
					// The NEWER fetch, which succeeds with a different answer.
					return HttpResponse.json({
						declaration: {
							...declared,
							schedule: { ...declared.schedule, cron: "45 * * * *" },
						},
						delegations: [],
						children: [],
					});
				},
			),
		);
		return () => release403?.();
	}

	it("does not erase the newer answer it was superseded by", async () => {
		const release = overlapping();
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");

		// The old fetch starts and is left in flight.
		const stale = client
			.refetchQueries({ queryKey: KEY, exact: true })
			.catch(() => undefined);
		// The newer fetch supersedes it and lands.
		await act(async () => {
			await client.refetchQueries({ queryKey: KEY, exact: true });
		});
		await screen.findByText("45 * * * *");

		// Now the superseded request finally answers 403.
		release();
		await act(async () => {
			await stale;
		});

		const state = client.getQueryState([...KEY]);
		expect(state?.status).toBe("success");
		// The newer answer is still there, and still on screen.
		expect(client.getQueryData([...KEY])).toBeTruthy();
		expect(screen.getByText("45 * * * *")).toBeTruthy();
		expect(screen.queryByText("This source declares no trigger.")).toBeNull();
	});

	// The deletion itself must survive the guard: a refusal with nothing newer
	// behind it still clears, including when no component is mounted to see it.
	it("still deletes a refusal that nothing newer replaced, with the view unmounted", async () => {
		let denied = false;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				denied
					? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
					: HttpResponse.json({
							declaration: declared,
							delegations: [],
							children: [],
						}),
			),
		);
		const { client } = renderInApp(<SourceTriggerSchedules orgId="org-1" />);
		await screen.findByText("*/30 * * * *");
		expect(client.getQueryData([...KEY])).toBeTruthy();

		cleanup();
		denied = true;
		await act(async () => {
			await client
				.refetchQueries({ queryKey: KEY, exact: true, type: "all" })
				.catch(() => undefined);
		});

		expect(client.getQueryData([...KEY])).toBeUndefined();
	});
});
