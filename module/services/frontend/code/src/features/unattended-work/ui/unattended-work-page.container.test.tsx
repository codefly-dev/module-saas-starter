import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { delay, HttpResponse, http, type JsonBodyType } from "msw";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { UnattendedWorkPage } from "./unattended-work-page";

// The page as an organization administrator drives it, against the platform's
// own operations: see what runs with nobody signed in and whose access it
// uses, stop that access, and have the next run refused.
//
// The refusal is the half worth a test. Everything up to it is a page showing
// what the platform answered; the last step is the property the whole design
// rests on: that stopping an access lands on the NEXT run.

// An organization administrator with NO platform role, because that is who the
// operations behind this page admit.
const session = vi.hoisted(() => ({
	organizationId: "org-1",
	isAuthenticated: true,
	user: { id: "me", name: "Sam Admin", email: "sam@example.com" },
	orgRole: "admin" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: async () => {},
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => session }));
vi.mock("sonner", () => ({
	toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

const SOURCE = "aaaaaaaa-0000-4000-8000-000000000001";
const OWNER = "cccccccc-0000-4000-8000-000000000001";
const DELEGATION = "bbbbbbbb-0000-4000-8000-000000000001";

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
	// The declaration, spelled as an installation document carries it: the
	// lowercase policy values the decode admits, a deadline in nanoseconds,
	// and `source` rather than the read API's `source_delegation`.
	schedule: {
		cron: "*/30 * * * *",
		timezone: "UTC",
		missedRun: "skip",
		overlap: "skip",
		deadline: "600000000000",
		inputJson: `{"org_id":"org-1","source_id":"${SOURCE}"}`,
	},
	identity: { source: SOURCE },
	digest: "4f1c",
	nextOccurrenceAt: "2026-10-12T18:30:00Z",
};

const reference = {
	reference: SOURCE,
	delegationId: DELEGATION,
	module: "docstore",
	binding: "occurrences",
	audience: "docstore",
	tenant: "org-1",
	ownerPrincipalId: OWNER,
	scopes: ["collections:write"],
	active: true,
	createdAt: "2026-10-12T09:00:00Z",
};

const child = {
	record: "dddddddd-0000-4000-8000-000000000001",
	delegationId: DELEGATION,
	module: "docstore",
	binding: "occurrences",
	audience: "docstore",
	scopes: ["collections:write"],
	lookup: false,
	mintedAt: "2026-10-12T09:05:00Z",
};

beforeEach(() => {
	session.organizationId = "org-1";
	session.orgRole = "admin";
	session.platformRole = undefined;
	vi.clearAllMocks();
});
afterEach(cleanup);

/** The two reads the page makes, with the declaration answer under test. */
function serve(
	declaration: JsonBodyType,
	reconcileAnswer: () => Response = () =>
		HttpResponse.json({ jobId: "job-1", effectId: "e-1", status: "committed" }),
) {
	const calls = { declaration: 0, delegate: 0, revoke: 0, reconcile: 0 };
	server.use(
		http.post(rpc("DatasourceService", "ListSources"), () =>
			HttpResponse.json(sources),
		),
		http.post(rpc("OrganizationService", "ListMembers"), () =>
			HttpResponse.json({
				members: [{ userId: OWNER, userEmail: "jane@example.com" }],
			}),
		),
		http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () => {
			calls.declaration++;
			return HttpResponse.json(declaration);
		}),
		http.post(rpc("DatasourceService", "DelegateSourceTriggers"), () => {
			calls.delegate++;
			return HttpResponse.json({ delegations: [reference] });
		}),
		http.post(rpc("DatasourceService", "RevokeSourceDelegation"), () => {
			calls.revoke++;
			return HttpResponse.json({ delegation: { id: DELEGATION } });
		}),
		http.post(rpc("DatasourceService", "ReconcileSource"), () => {
			calls.reconcile++;
			return reconcileAnswer();
		}),
	);
	return calls;
}

const answered = {
	declaration: declared,
	delegations: [reference],
	children: [child],
};

const refuse = (code: string, message: string, status: number) => () =>
	HttpResponse.json({ code, message }, { status });

describe("UnattendedWorkPage", () => {
	it("says what runs, how often and with whose access, in an administrator's words", async () => {
		serve(answered);
		renderInApp(<UnattendedWorkPage />);

		expect(
			await screen.findByText(
				"Refreshes this data source so its content stays up to date",
			),
		).toBeTruthy();
		expect(screen.getByRole("heading", { level: 1 }).textContent).toBe(
			"Unattended work",
		);
		expect(screen.getByText("Every 30 minutes")).toBeTruthy();
		expect(screen.getByText("UTC")).toBeTruthy();
		expect(
			screen.getByText("Skipped. The next run happens at its usual time."),
		).toBeTruthy();
		expect(screen.getByText("The new run is skipped.")).toBeTruthy();
		expect(screen.getByText("10 minutes")).toBeTruthy();
		expect(screen.getByText("A person's access, listed below")).toBeTruthy();

		// Whose access: the module, the person by name, and what it may do.
		for (const header of ["Person", "What it may do", "State"]) {
			expect(screen.getByRole("columnheader", { name: header })).toBeTruthy();
		}
		expect(await screen.findByText("jane@example.com")).toBeTruthy();
		expect(screen.getAllByText("Change collections").length).toBe(2);
		expect(screen.getByText("In use")).toBeTruthy();

		// And the run on record.
		expect(screen.getByRole("columnheader", { name: "Started" })).toBeTruthy();
		expect(screen.getByText("Did the work")).toBeTruthy();
	});

	it("keeps the platform's own names under Technical details", async () => {
		serve(answered);
		renderInApp(<UnattendedWorkPage />);
		await screen.findByText("Every 30 minutes");

		const page = document.body.textContent ?? "";
		for (const technical of [
			"saas_datasource_reconcile",
			"ReconcileSource",
			"*/30 * * * *",
			"collections:write",
			"docstore/occurrences",
			OWNER,
			DELEGATION,
		]) {
			expect(page).not.toContain(technical);
		}
		expect(page).not.toMatch(/delegation|occurrence|minted|Work Context/i);

		fireEvent.click(screen.getByRole("button", { name: "Technical details" }));
		expect(await screen.findByText("saas_datasource_reconcile")).toBeTruthy();
		expect(
			screen.getByText("/saas.accounts.v1.DatasourceService/ReconcileSource"),
		).toBeTruthy();
		expect(screen.getByText("*/30 * * * *")).toBeTruthy();
		expect(screen.getAllByText("collections:write").length).toBeGreaterThan(0);
	});

	it("stops an access only after a question naming the person and the source", async () => {
		const calls = serve(answered);
		renderInApp(<UnattendedWorkPage />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Stop using this access" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await waitFor(() =>
			expect(dialog.textContent).toContain(
				"Stop using jane@example.com's access for acme/handbook?",
			),
		);
		expect(dialog.textContent).toContain(
			"Unattended work by docstore on acme/handbook will be refused from its next run.",
		);
		expect(dialog.textContent).toContain(
			"Reconnecting the source restores this access.",
		);
		// Asking is not doing.
		expect(calls.revoke).toBe(0);

		fireEvent.click(
			within(dialog).getByRole("button", { name: "Stop using this access" }),
		);
		await waitFor(() => expect(calls.revoke).toBe(1));
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledWith(
				"Access stopped for acme/handbook",
			),
		);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("leaves the access in use when the question is cancelled", async () => {
		const calls = serve(answered);
		renderInApp(<UnattendedWorkPage />);

		fireEvent.click(
			await screen.findByRole("button", { name: "Stop using this access" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(calls.revoke).toBe(0);
	});

	it("sets up access, runs now, and reports a refused run as refused", async () => {
		const calls = serve(
			answered,
			refuse("permission_denied", "the source delegation is revoked", 403),
		);
		renderInApp(<UnattendedWorkPage />);

		fireEvent.click(
			await screen.findByRole("button", { name: /Set up access/ }),
		);
		await waitFor(() => expect(calls.delegate).toBe(1));
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledWith(
				"Access set up for acme/handbook",
			),
		);

		// The run is refused, and the page reports the platform's refusal rather
		// than a run that never happened.
		fireEvent.click(screen.getByRole("button", { name: /Run now/ }));
		await waitFor(() => expect(calls.reconcile).toBe(1));
		await waitFor(() => expect(toast.error).toHaveBeenCalled());
		expect(String(vi.mocked(toast.error).mock.calls[0][0])).toContain(
			"Couldn't start a run",
		);
		expect(toast.success).not.toHaveBeenCalledWith(
			"Run started for acme/handbook",
		);
	});

	it("starts a run and says so", async () => {
		const calls = serve(answered);
		renderInApp(<UnattendedWorkPage />);

		const run = await screen.findByRole("button", { name: /Run now/ });
		await waitFor(() =>
			expect((run as HTMLButtonElement).disabled).toBe(false),
		);
		fireEvent.click(run);
		await waitFor(() => expect(calls.reconcile).toBe(1));
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledWith(
				"Run started for acme/handbook",
			),
		);
	});

	it("says a source has no schedule and no access, and does not offer to run it", async () => {
		serve({
			declaration: {
				release: "saas_datasource_reconcile",
				operation: "/saas.accounts.v1.DatasourceService/ReconcileSource",
				undeclarableReason: "the source has no reconcile schedule",
			},
			delegations: [],
			children: [],
		});
		renderInApp(<UnattendedWorkPage />);

		expect(await screen.findByText("This source has no schedule")).toBeTruthy();
		// The platform's own reason is an engineer's sentence; it is not shown.
		expect(
			screen.queryByText(/no reconcile schedule/, { exact: false }),
		).toBeNull();
		expect(
			screen.getByText("No one's access is set up for this source"),
		).toBeTruthy();
		expect(screen.getByText("No runs yet")).toBeTruthy();
		expect(screen.queryByRole("table")).toBeNull();
		// With no access in use there is nothing a run could use.
		const run = screen.getByRole("button", { name: /Run now/ });
		expect((run as HTMLButtonElement).disabled).toBe(true);
		// The first action is offered where the absence is said.
		expect(screen.getByRole("button", { name: /Set up access/ })).toBeTruthy();
	});

	it("shows loading tables, never the empty states, while the read is in flight", async () => {
		serve(answered);
		server.use(
			http.post(
				rpc("DatasourceService", "GetSourceTriggerDeclaration"),
				async () => {
					await delay(600);
					return HttpResponse.json(answered);
				},
			),
		);
		renderInApp(<UnattendedWorkPage />);

		expect((await screen.findAllByLabelText("Loading table")).length).toBe(2);
		expect(
			screen.queryByText("No one's access is set up for this source"),
		).toBeNull();
		expect(screen.queryByText("No runs yet")).toBeNull();
		expect(screen.queryByText("This source has no schedule")).toBeNull();
		expect(await screen.findByText("Every 30 minutes")).toBeTruthy();
	});

	it("tells a reader who may not see a source's work who can grant it, with no retry", async () => {
		serve(answered);
		server.use(
			http.post(
				rpc("DatasourceService", "GetSourceTriggerDeclaration"),
				refuse("permission_denied", "requires role ORG_ADMIN", 403),
			),
		);
		renderInApp(<UnattendedWorkPage />);

		const refusal = await screen.findByRole("alert");
		expect(refusal.textContent).toContain(
			"You don't have access to unattended work for acme/handbook",
		);
		expect(refusal.textContent).toContain(
			"Ask an organization administrator to give you access.",
		);
		expect(refusal.textContent).not.toContain("ORG_ADMIN");
		expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
		expect(screen.queryByText("No runs yet")).toBeNull();
	});

	it("says a failed read failed, in plain words, and retries it", async () => {
		serve(answered);
		let healthy = false;
		server.use(
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				healthy
					? HttpResponse.json(answered)
					: HttpResponse.json(
							{ code: "internal", message: "pq: connection reset" },
							{ status: 500 },
						),
			),
		);
		renderInApp(<UnattendedWorkPage />);

		const failure = await screen.findByRole("alert");
		expect(failure.textContent).toContain(
			"Couldn't load unattended work for acme/handbook",
		);
		expect(failure.textContent).not.toContain("connection reset");
		expect(screen.queryByText("No runs yet")).toBeNull();

		healthy = true;
		fireEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(await screen.findByText("Every 30 minutes")).toBeTruthy();
	});

	it("offers to connect a data source when the organization has none", async () => {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json({ datasources: [] }),
			),
		);
		renderInApp(<UnattendedWorkPage />);

		expect(await screen.findByText("No data source is connected")).toBeTruthy();
		expect(
			screen
				.getByRole("button", { name: "Connect a data source" })
				.getAttribute("href"),
		).toBe("/admin/datasources");
		expect(screen.queryByRole("button", { name: /Run now/ })).toBeNull();
	});

	it("tells refused from failed when the data sources themselves cannot be read", async () => {
		server.use(
			http.post(
				rpc("DatasourceService", "ListSources"),
				refuse("permission_denied", "requires role ORG_ADMIN", 403),
			),
		);
		renderInApp(<UnattendedWorkPage />);
		const refusal = await screen.findByRole("alert");
		expect(refusal.textContent).toContain(
			"You don't have access to this organization's data sources",
		);
		expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
		expect(screen.queryByText("No data source is connected")).toBeNull();
		cleanup();

		server.use(
			http.post(
				rpc("DatasourceService", "ListSources"),
				refuse("internal", "pq: connection reset", 500),
			),
		);
		renderInApp(<UnattendedWorkPage />);
		const failure = await screen.findByRole("alert");
		expect(failure.textContent).toContain(
			"Couldn't load this organization's data sources",
		);
		expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
		expect(screen.queryByText("No data source is connected")).toBeNull();
	});
});
