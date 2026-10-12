import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Page from "@/app/admin/platform/unattended-work/page";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// The playground's walk, driven through the host's own operations: bind the
// identity an occurrence borrows, see the children minted against it, revoke
// it, and watch the next occurrence refused.
//
// The refusal is the half worth a test. Everything up to it is a page showing
// what the host answered; the last step is the property the whole design rests
// on — that a revocation lands on the NEXT occurrence, with nothing held from
// the one before it.

const session = vi.hoisted(() => ({
	organizationId: "org-1",
	isAuthenticated: true,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: "super_admin" as PlatformRole | undefined,
	switchOrganization: async () => {},
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => session }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const SOURCE = "aaaaaaaa-0000-4000-8000-000000000001";
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
	schedule: {
		cron: "*/30 * * * *",
		timezone: "UTC",
		missedRun: "MISSED_RUN_POLICY_SKIP",
		overlap: "OVERLAP_POLICY_SKIP",
		deadlineMillis: "600000",
	},
	identity: { sourceDelegation: SOURCE },
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
	ownerPrincipalId: "cccccccc-0000-4000-8000-000000000001",
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
	session.platformRole = "super_admin";
	vi.clearAllMocks();
});
afterEach(cleanup);

/** The two reads the page makes, with the declaration answer under test. */
function serve(declaration: JsonBodyType) {
	const calls = { declaration: 0, delegate: 0, revoke: 0, reconcile: 0 };
	server.use(
		http.post(rpc("DatasourceService", "ListSources"), () =>
			HttpResponse.json(sources),
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
			return HttpResponse.json(
				{
					code: "permission_denied",
					message: "the source delegation is revoked",
				},
				{ status: 403 },
			);
		}),
	);
	return calls;
}

describe("UnattendedWorkPlayground", () => {
	it("shows the trigger a source declares and the identity its occurrences borrow", async () => {
		serve({
			declaration: declared,
			delegations: [reference],
			children: [child],
		});
		renderInApp(<Page />);

		// The declaration, as an executing module would install it.
		expect(await screen.findByText("saas_datasource_reconcile")).toBeTruthy();
		expect(screen.getByText("*/30 * * * *")).toBeTruthy();
		expect(screen.getByText("MISSED_RUN_POLICY_SKIP")).toBeTruthy();
		// Which arm it names, said in words: a declaration that named neither
		// would have its occurrences refused, and the page says so rather than
		// leaving the field blank.
		expect(screen.getByText("A delegation reference")).toBeTruthy();

		// The reference an operator bound, with whose authority it carries and
		// exactly what it allows — never the module's own authority.
		const [row] = screen.getAllByText("docstore/occurrences");
		expect(row).toBeTruthy();
		expect(screen.getAllByText("collections:write").length).toBeGreaterThan(0);
		expect(screen.getByText("Live")).toBeTruthy();

		// And one child minted against it. This is the only record of a
		// capability: nothing stores one.
		expect(screen.getByText("Produces the effect")).toBeTruthy();
	});

	it("mints a reference, then refuses the next occurrence once it is revoked", async () => {
		const calls = serve({
			declaration: declared,
			delegations: [reference],
			children: [child],
		});
		renderInApp(<Page />);

		fireEvent.click(
			await screen.findByRole("button", { name: /Mint a reference/ }),
		);
		await waitFor(() => expect(calls.delegate).toBe(1));
		expect(toast.success).toHaveBeenCalledWith("Delegation reference minted");

		// Revoking takes the authority away. Nothing has to be stopped for that
		// to take effect: the host re-checks the delegation on every mint.
		fireEvent.click(await screen.findByRole("button", { name: "Revoke" }));
		await waitFor(() => expect(calls.revoke).toBe(1));

		// The next occurrence is refused, and the page reports the host's own
		// refusal rather than reporting a pass that never ran.
		fireEvent.click(
			await screen.findByRole("button", { name: /Run one occurrence/ }),
		);
		await waitFor(() => expect(calls.reconcile).toBe(1));
		await waitFor(() => expect(toast.error).toHaveBeenCalled());
		expect(String(vi.mocked(toast.error).mock.calls[0][0])).toContain(
			"revoked",
		);
	});

	it("says a source on no schedule declares no trigger, rather than showing an empty one", async () => {
		serve({
			declaration: {
				release: "saas_datasource_reconcile",
				operation: "/saas.accounts.v1.DatasourceService/ReconcileSource",
				undeclarableReason: "the source has no reconcile schedule",
			},
			delegations: [],
			children: [],
		});
		renderInApp(<Page />);

		expect(
			await screen.findByText("the source has no reconcile schedule"),
		).toBeTruthy();
		// With no live reference there is nothing an occurrence could borrow, so
		// the page does not offer to run one.
		const run = await screen.findByRole("button", {
			name: /Run one occurrence/,
		});
		expect((run as HTMLButtonElement).disabled).toBe(true);
		expect(
			screen.getByText(/No reference is bound to this source's triggers/),
		).toBeTruthy();
	});
});
