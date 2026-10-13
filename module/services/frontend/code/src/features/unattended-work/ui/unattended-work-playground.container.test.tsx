import { onlineManager, QueryClient } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Page from "@/app/admin/unattended-work/page";
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

// An organization administrator with NO platform role, because that is who the
// RPCs behind this page admit (issue #1112, m4). The page asked for a platform
// super administrator, which hid an org admin's own organization from them.
const session = vi.hoisted(() => ({
	organizationId: "org-1",
	isAuthenticated: true,
	orgRole: "admin" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
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
	// The declaration, spelled as an installation document carries it: the
	// lowercase policy values the decode admits, a deadline in nanoseconds,
	// and `source` rather than the read API's `source_delegation`.
	schedule: {
		cron: "*/30 * * * *",
		timezone: "UTC",
		missedRun: "skip",
		overlap: "skip",
		deadline: "600000000000",
		inputJson: '{"org_id":"org-1","source_id":"' + SOURCE + '"}',
	},
	identity: { source: SOURCE },
	// Whose delegation the reference resolves to. The arm cannot carry it (the
	// value is the source id), and it is NOT a claim that this module executes
	// the release — see declaredIdentity (issue #1121, F1).
	identityHolder: "docstore",
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
	session.orgRole = "admin";
	session.platformRole = undefined;
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
		// The policy values are the document's, not the read API's enum
		// spelling: what the page shows is what a composition pastes in.
		expect(screen.getAllByText("skip").length).toBe(2);
		expect(screen.getByText("10m")).toBeTruthy();
		// Which arm it names, said in words: a declaration that named neither
		// would have its occurrences refused, and the page says so rather than
		// leaving the field blank.
		// It names the HOLDER, which the arm's own value cannot carry, and
		// refuses to claim that the holder is the module which executes this
		// release — nothing the host holds declares that, so a source whose
		// only self-addressed delegation belongs to another module would read
		// as ready while the composed executor's mint was refused
		// (issue #1121, F1).
		expect(
			screen.getByText(/A delegation reference held by docstore/),
		).toBeTruthy();
		expect(screen.getByText(/unverified/)).toBeTruthy();

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

	it("admits an organization administrator and refuses a plain member", async () => {
		serve({
			declaration: declared,
			delegations: [reference],
			children: [child],
		});
		// The RPCs behind the page are ORG_ADMIN and org-scoped, so this is the
		// role the gate must match. Before this it required a platform super
		// administrator and told the reader it reached every tenant.
		renderInApp(<Page />);
		expect(await screen.findByText("Unattended work")).toBeTruthy();
		cleanup();

		session.orgRole = "member";
		renderInApp(<Page />);
		expect(
			await screen.findByText("Organization administrator required"),
		).toBeTruthy();
		expect(screen.queryByText("Unattended work")).toBeNull();
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

// THE PLAYGROUND IS THE OTHER CONSUMER OF THE SHARED DECLARATION QUERY
// (issue #1121, r22 F1 closure). It already withheld retained rows on refusal,
// which is the half that was right; what it did not do is SAY so — the facts
// rendered as em dashes, which read as "this host declares nothing" to a viewer
// who is simply not allowed to see it. The fix to the shared query deletes the
// snapshot, so this card must now explain the state rather than dash it.
describe("the playground's declaration card when the read does not succeed", () => {
	function serveDeclaration(handler: () => Response) {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				handler(),
			),
		);
	}

	it("says a refused declaration was refused, rather than dashing its facts", async () => {
		serveDeclaration(() =>
			HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
		);
		renderInApp(<Page />);
		await screen.findByText("acme/handbook");

		// Scoped to the DECLARATION card: the delegations card below it carries
		// its own refusal message, so a page-wide match would pass without this
		// card saying anything at all.
		const card = await waitFor(() => {
			const found = document.querySelector<HTMLElement>(
				'[data-slot="declared-trigger"]',
			);
			expect(found).not.toBeNull();
			return found as HTMLElement;
		});
		await waitFor(() =>
			expect(within(card).getByText(/don.t have permission/i)).toBeTruthy(),
		);
		// No invented facts: the release and operation are the host's answer,
		// and it gave none.
		expect(screen.queryByText("saas_datasource_reconcile")).toBeNull();
	});

	it("recovers the declaration card when a later read succeeds", async () => {
		let denied = true;
		serveDeclaration(() =>
			denied
				? HttpResponse.json({ code: "permission_denied" }, { status: 403 })
				: HttpResponse.json({
						declaration: declared,
						delegations: [reference],
						children: [child],
					}),
		);
		const { client } = renderInApp(<Page />);
		// Scoped to the declaration card: the delegations card carries the same
		// sentence about its own read, so an unscoped match is ambiguous.
		const card = await waitFor(() => {
			const found = document.querySelector<HTMLElement>(
				'[data-slot="declared-trigger"]',
			);
			expect(found).not.toBeNull();
			return found as HTMLElement;
		});
		await waitFor(() =>
			expect(within(card).getByText(/don.t have permission/i)).toBeTruthy(),
		);

		denied = false;
		await act(async () => {
			await client.refetchQueries({ queryKey: ["source-trigger-declaration"] });
		});

		await screen.findByText("saas_datasource_reconcile");
		expect(
			within(
				document.querySelector('[data-slot="declared-trigger"]') as HTMLElement,
			).queryByText(/don.t have permission/i),
		).toBeNull();
	});
});

// A READ IN FLIGHT CLAIMS NOTHING ABOUT THE IDENTITY EITHER (issue #1121).
//
// The card learned to say which state its read was in and still fell into the
// facts list while the read was in flight: the `<dl>` rendered dashes, and the
// Identity row, computed from no declaration at all, said "None — occurrences
// would be refused". That is the same false statement about authority the
// schedule row was fixed for, surviving in the other consumer because the
// branch tested `!declaration && !read.isFetching` — true only once the read
// had finished, and so false for the whole of the pending window.
describe("the playground's declaration card while the read is in flight", () => {
	it("claims no identity before the declaration arrives", async () => {
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
						delegations: [reference],
						children: [],
					});
				},
			),
		);
		renderInApp(<Page />);

		const card = await waitFor(() => {
			const found = document.querySelector<HTMLElement>(
				'[data-slot="declared-trigger"]',
			);
			expect(found).not.toBeNull();
			return found as HTMLElement;
		});
		// Nothing about the identity, and no facts spelled as dashes, while the
		// host has not answered.
		expect(
			within(card).queryByText("None — occurrences would be refused"),
		).toBeNull();
		expect(within(card).queryByText("Identity")).toBeNull();

		release?.();
		await screen.findByText("saas_datasource_reconcile");
	});
});

// AND THE SAME PAUSED READ IN THE PLAYGROUND'S CARD (issue #1121, the
// read-lifecycle review). Offline, a cold query parks at status `pending` with
// fetchStatus `paused`, and `isFetching` is FALSE — so the card's gate read it
// as "finished, nothing came back" and said this source declares no trigger
// about a read that never left the gate. The source list beside it already
// distinguishes paused from disabled with `isPending && fetchStatus !== "idle"`.
describe("the playground's declaration card when the read is paused", () => {
	afterEach(() => onlineManager.setOnline(true));

	it("claims nothing while paused, and recovers when online", async () => {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				HttpResponse.json({
					declaration: declared,
					delegations: [reference],
					children: [],
				}),
			),
		);
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		client.setQueryData(["unattended-sources", "org-1"], sources.datasources);
		onlineManager.setOnline(false);

		renderInApp(<Page />, client);
		const card = await waitFor(() => {
			const found = document.querySelector<HTMLElement>(
				'[data-slot="declared-trigger"]',
			);
			expect(found).not.toBeNull();
			return found as HTMLElement;
		});
		await waitFor(() => {
			const state = client.getQueryState([
				"source-trigger-declaration",
				"org-1",
				SOURCE,
			]);
			expect(state?.fetchStatus).toBe("paused");
		});

		expect(
			within(card).queryByText("This source declares no trigger."),
		).toBeNull();
		expect(
			within(card).queryByText("None — occurrences would be refused"),
		).toBeNull();

		onlineManager.setOnline(true);
		await screen.findByText("saas_datasource_reconcile");
	});
});

// THE CHILDREN CARD AND THE ACTION'S EXPLANATION READ THE SAME RESPONSE
// (issue #1121, r24). Both asserted an absence straight off an array length,
// so a read that had not answered — cold refusal, held request, paused offline
// — produced "No child has been minted against this source's references" and a
// tooltip reading "No live reference: an occurrence would be refused". Neither
// is a fact the host had supplied; a true empty is only knowable after a
// successful read.
//
// The button's disabled state is deliberately NOT part of this fix: refusing to
// act on an unknown reference set is correct. What changes is what the operator
// is TOLD about why.
const CHILDREN_EMPTY =
	"No child has been minted against this source's references.";
const NO_LIVE_REFERENCE = "No live reference: an occurrence would be refused";

describe("the children card and the occurrence button on a read that has not answered", () => {
	afterEach(() => onlineManager.setOnline(true));

	function held() {
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
						delegations: [reference],
						children: [child],
					});
				},
			),
		);
		return () => release?.();
	}

	function refuse() {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
			),
		);
	}

	it("claims no absent child while the request is still held", async () => {
		const release = held();
		renderInApp(<Page />);
		await screen.findByText("acme/handbook");

		expect(screen.queryByText(CHILDREN_EMPTY)).toBeNull();
		expect(screen.queryByTitle(NO_LIVE_REFERENCE)).toBeNull();

		release();
		await screen.findByText("docstore");
	});

	it("says a refused read was refused rather than reporting no children", async () => {
		refuse();
		renderInApp(<Page />);
		await screen.findByText("acme/handbook");

		await waitFor(() => expect(screen.queryByText(CHILDREN_EMPTY)).toBeNull());
		expect(screen.queryByTitle(NO_LIVE_REFERENCE)).toBeNull();
		// The action stays refused — authorization is unchanged — and says why.
		const run = screen.getByRole("button", { name: /Run one occurrence/ });
		expect(run.hasAttribute("disabled")).toBe(true);
		expect(run.getAttribute("title")).toMatch(/permission/i);
	});

	it("claims no absent child while the read is paused offline", async () => {
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () =>
				HttpResponse.json({
					declaration: declared,
					delegations: [reference],
					children: [child],
				}),
			),
		);
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		client.setQueryData(["unattended-sources", "org-1"], sources.datasources);
		onlineManager.setOnline(false);
		renderInApp(<Page />, client);
		await screen.findByText("acme/handbook");

		await waitFor(() => {
			const state = client.getQueryState([
				"source-trigger-declaration",
				"org-1",
				SOURCE,
			]);
			expect(state?.fetchStatus).toBe("paused");
		});
		expect(screen.queryByText(CHILDREN_EMPTY)).toBeNull();
		expect(screen.queryByTitle(NO_LIVE_REFERENCE)).toBeNull();

		onlineManager.setOnline(true);
		await screen.findByText("docstore");
	});

	// A TRUE EMPTY IS STILL SAID — after a read that succeeded. Suppressing it
	// everywhere would trade one false statement for a blank card.
	it("reports no children once a successful read says so", async () => {
		serve({ declaration: declared, delegations: [reference], children: [] });
		renderInApp(<Page />);

		expect(await screen.findByText(CHILDREN_EMPTY)).toBeTruthy();
	});

	// And a transient failure keeps what the last good read said, under a
	// notice, rather than reporting an absence it cannot see.
	it("keeps the children a transient failure left behind", async () => {
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
							delegations: [reference],
							children: [child],
						}),
			),
		);
		const { client } = renderInApp(<Page />);
		await screen.findByText("docstore");

		down = true;
		await act(async () => {
			await client
				.refetchQueries({ queryKey: ["source-trigger-declaration"] })
				.catch(() => undefined);
		});

		expect(screen.getByText("docstore")).toBeTruthy();
		expect(screen.queryByText(CHILDREN_EMPTY)).toBeNull();
	});
});

// ALL THREE CARDS READ ONE RESPONSE, SO ALL THREE ARE CLASSIFIED THE SAME WAY
// (issue #1121, r24/r25). The references card was the third: it tested
// `outcome === "empty"`, and `empty` from the classifier means "nothing has
// failed", NOT "the read succeeded" — so a pending read said "No reference is
// bound to this source's triggers. An occurrence would be refused."
//
// And the paused-RETRY state reaches these cards too: initial success, a
// refetch refused (the query function deletes the answer), the retry paused
// offline. Status `success`, fetchStatus `paused`, data undefined, error null.
const REFERENCES_EMPTY =
	"No reference is bound to this source's triggers. An occurrence would be refused.";

describe("the cards on one response, in the states that are not an answer", () => {
	afterEach(() => onlineManager.setOnline(true));

	it("claims no absent reference while the read is still in flight", async () => {
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
						delegations: [reference],
						children: [child],
					});
				},
			),
		);
		renderInApp(<Page />);
		await screen.findByText("acme/handbook");

		expect(screen.queryByText(REFERENCES_EMPTY)).toBeNull();

		release?.();
		await screen.findByText("saas_datasource_reconcile");
	});

	// The true empty is still said once a successful read says so.
	it("reports no reference once a successful read says so", async () => {
		serve({ declaration: declared, delegations: [], children: [] });
		renderInApp(<Page />);

		expect(await screen.findByText(REFERENCES_EMPTY)).toBeTruthy();
	});

	it("claims nothing in the state a refused refetch and a paused retry leave", async () => {
		let call = 0;
		server.use(
			http.post(rpc("DatasourceService", "ListSources"), () =>
				HttpResponse.json(sources),
			),
			http.post(rpc("DatasourceService", "GetSourceTriggerDeclaration"), () => {
				call++;
				if (call === 1) {
					return HttpResponse.json({
						declaration: declared,
						delegations: [reference],
						children: [child],
					});
				}
				if (call === 2) {
					onlineManager.setOnline(false);
					return HttpResponse.json(
						{ code: "permission_denied" },
						{ status: 403 },
					);
				}
				return HttpResponse.json({
					declaration: declared,
					delegations: [reference],
					children: [child],
				});
			}),
		);
		const client = new QueryClient({
			defaultOptions: { queries: { retry: 1, retryDelay: 0 } },
		});
		renderInApp(<Page />, client);
		await screen.findByText("saas_datasource_reconcile");

		const refetching = client
			.refetchQueries({
				queryKey: ["source-trigger-declaration", "org-1", SOURCE],
				exact: true,
			})
			.catch(() => undefined);

		await waitFor(() => {
			const state = client.getQueryState([
				"source-trigger-declaration",
				"org-1",
				SOURCE,
			]);
			expect(state?.status).toBe("success");
			expect(state?.fetchStatus).toBe("paused");
			expect(state?.data).toBeUndefined();
			expect(state?.error).toBeNull();
		});

		// None of the three cards asserts an absence in this state.
		expect(screen.queryByText(REFERENCES_EMPTY)).toBeNull();
		expect(screen.queryByText(CHILDREN_EMPTY)).toBeNull();
		expect(screen.queryByText("This source declares no trigger.")).toBeNull();
		expect(screen.queryByTitle(NO_LIVE_REFERENCE)).toBeNull();

		onlineManager.setOnline(true);
		await act(async () => {
			await refetching;
		});
		await screen.findByText("saas_datasource_reconcile");
	});
});
