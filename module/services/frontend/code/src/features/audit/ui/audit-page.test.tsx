import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// AuditPage reads the active tenant from the signed access token via useAuth,
// which throws outside an AuthProvider. Supply a stable org so the page can
// resolve its actor directory.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1" }),
}));

import { AuditPage } from "./audit-page";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ platformRole: "super_admin", organizationId: "org-1" }),
}));

afterEach(cleanup);

function auditEvent(overrides: Record<string, unknown> = {}) {
	return {
		id: "evt-1",
		actorId: "a3f81c2e-0000-4000-8000-000000000001",
		actorType: "user",
		eventType: "saas.auth.login",
		category: "auth",
		resource: "session",
		resourceId: "sess-1",
		orgId: "org-1",
		payload: {},
		ipAddress: "203.0.113.7",
		...overrides,
	};
}

describe("AuditPage admin container", () => {
	it("renders the audit events the service returns", async () => {
		server.use(
			http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
				HttpResponse.json({ types: [] }),
			),
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ buckets: [] }),
			),
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({ events: [auditEvent()], totalCount: 1 }),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("203.0.113.7")).toBeTruthy();
		// The table humanizes the event type without its namespace segment.
		expect(screen.getByText("Auth Login")).toBeTruthy();
		expect(screen.queryByText("Saas Auth Login")).toBeNull();
	});

	// "Who did this" and "what did they do it through" are separate questions,
	// so the client rides beside the actor rather than replacing it, and a call
	// from the host's own session must show no client at all.
	it("names the client a call was made through, and none for a web session", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({
					events: [
						auditEvent({ id: "evt-client", clientId: "example-console" }),
						auditEvent({ id: "evt-web" }),
					],
					totalCount: 2,
				}),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("example-console")).toBeTruthy();
		expect(screen.getAllByText(/^via/)).toHaveLength(1);
	});

	it("renders an actor by display name rather than by truncated id", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({ events: [auditEvent()], totalCount: 1 }),
			),
			http.post(rpc("PrincipalService", "ListPrincipals"), () =>
				HttpResponse.json({
					principals: [
						{
							id: "a3f81c2e-0000-4000-8000-000000000001",
							displayName: "Jane Doe",
							kind: "PRINCIPAL_KIND_HUMAN",
						},
					],
					nextPageToken: "",
				}),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("Jane Doe")).toBeTruthy();
		expect(screen.queryByText("a3f81c2e...")).toBeNull();
		// actor_type stays beside the name: an agent's action must not read as
		// a person's.
		expect(screen.getByText("user")).toBeTruthy();
	});

	it("shows an unavailable label for a principal the directory misses", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({
					events: [auditEvent({ actorType: "agent" })],
					totalCount: 1,
				}),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("Actor unavailable")).toBeTruthy();
		expect(screen.getByText("agent")).toBeTruthy();
	});

	// The Actor column accesses actorId but renders a name, so the default
	// sort ordered rows by raw uuid — an order with no relation to the names
	// on screen.
	it("sorts the Actor column by the name it renders, not by the raw id", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({
					events: [
						auditEvent({
							id: "evt-1",
							actorId: "00000000-0000-4000-8000-00000000000a",
						}),
						auditEvent({
							id: "evt-2",
							actorId: "ffffffff-0000-4000-8000-00000000000f",
						}),
					],
					totalCount: 2,
				}),
			),
			http.post(rpc("PrincipalService", "ListPrincipals"), () =>
				HttpResponse.json({
					principals: [
						{
							id: "00000000-0000-4000-8000-00000000000a",
							displayName: "Zoe Zephyr",
						},
						{
							id: "ffffffff-0000-4000-8000-00000000000f",
							displayName: "Adam Ant",
						},
					],
					nextPageToken: "",
				}),
			),
		);
		renderInApp(<AuditPage />);
		await screen.findByText("Zoe Zephyr");

		fireEvent.click(screen.getByText("Actor"));

		const names = screen
			.getAllByRole("row")
			.slice(1)
			.map((row) => within(row).getByText(/Zoe Zephyr|Adam Ant/).textContent);
		expect(names).toEqual(["Adam Ant", "Zoe Zephyr"]);
	});

	// A failed directory walk used to be indistinguishable from an org with no
	// names: every actor rendered as an id with nothing to act on.
	it("says so when the actor directory cannot be loaded", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({ events: [auditEvent()], totalCount: 1 }),
			),
			http.post(rpc("PrincipalService", "ListPrincipals"), () =>
				HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
			),
		);
		renderInApp(<AuditPage />);
		expect(
			await screen.findByText(/Actor names could not be loaded/),
		).toBeTruthy();
	});

	// The summary cards double as filter controls for the tiles whose count
	// maps onto exactly one existing filter value.
	describe("clicking a summary card filters the list", () => {
		// A registry that actually reaches the page: the client reads `types`.
		const REGISTRY = [
			{
				name: "saas.user.registered",
				category: "identity",
				namespace: "saas",
				marksUserJoined: true,
			},
			{
				name: "saas.user.created",
				category: "identity",
				namespace: "saas",
				marksUserJoined: true,
			},
			// A membership provisioning, not a registration — the registry does
			// not mark it, so the tile must not count it.
			{
				name: "saas.auth.sso_jit_provisioned",
				category: "security",
				namespace: "saas",
				marksUserJoined: false,
			},
			{
				name: "saas.auth.login",
				category: "auth",
				namespace: "saas",
				marksUserJoined: false,
			},
		];
		// 900 auth logins, 40 identity registrations, 10 security events. The
		// aggregate is grouped by category then event type, so each bucket's
		// `keys` is [category, event_type].
		const BUCKETS = [
			{
				key: "auth",
				keys: ["auth", "saas.auth.login"],
				count: 900,
				metrics: {},
			},
			{
				key: "identity",
				keys: ["identity", "saas.user.registered"],
				count: 40,
				metrics: {},
			},
			{
				key: "security",
				keys: ["security", "saas.auth.sso_jit_provisioned"],
				count: 10,
				metrics: {},
			},
		];

		// A tile's number lives in the same card as its label, so read the card.
		function tile(label: string) {
			const card = screen.getByText(label).closest('[data-slot="card"]');
			if (!card) throw new Error(`no card for tile ${label}`);
			return card;
		}

		function serveAudit(
			options: {
				registry?: boolean;
				queryAuditLog?: ReturnType<typeof vi.fn>;
			} = {},
		) {
			server.use(
				http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
					HttpResponse.json({
						types: options.registry === false ? [] : REGISTRY,
					}),
				),
				http.post(
					rpc("AuditService", "AggregateAuditLog"),
					async ({ request }) => {
						const body = (await request.json()) as {
							groupBys?: string[];
							groupBy?: string;
						};
						const by = body.groupBys ?? (body.groupBy ? [body.groupBy] : []);
						const headline =
							by.length === 2 && by[0] === "category" && by[1] === "event_type";
						return HttpResponse.json({ buckets: headline ? BUCKETS : [] });
					},
				),
				http.post(rpc("AuditService", "QueryAuditLog"), async ({ request }) => {
					options.queryAuditLog?.(await request.json());
					return HttpResponse.json({ events: [], totalCount: 0 });
				}),
			);
		}

		it("filters to the security category and marks the tile pressed", async () => {
			const queryAuditLog = vi.fn();
			serveAudit({ queryAuditLog });
			renderInApp(<AuditPage />);

			const security = await screen.findByRole("button", {
				name: /Security events/,
			});
			expect(security.getAttribute("aria-pressed")).toBe("false");
			// The wire request omits a field left at its proto default, so an
			// unfiltered category reads as the field being absent, not "".
			await waitFor(() => expect(queryAuditLog).toHaveBeenCalled());
			expect(queryAuditLog.mock.calls.at(-1)?.[0]?.category).toBeFalsy();

			fireEvent.click(security);

			await waitFor(() =>
				expect(queryAuditLog.mock.calls.at(-1)?.[0]?.category).toBe("security"),
			);
			expect(security.getAttribute("aria-pressed")).toBe("true");
		});

		// The regression this pins: the tiles are controls, so each one's number
		// has to be the number its own click produces. Reading the in-scope
		// aggregate instead made "Events" display the filtered count (10 here)
		// while clicking it cleared the filters and showed 950.
		it("keeps the Events tile at the unfiltered total while a filter is applied", async () => {
			serveAudit();
			renderInApp(<AuditPage />);

			await waitFor(() => expect(tile("Events").textContent).toMatch(/950/));

			fireEvent.click(
				await screen.findByRole("button", { name: /Security events/ }),
			);

			await waitFor(() =>
				expect(
					screen
						.getByRole("button", { name: /Security events/ })
						.getAttribute("aria-pressed"),
				).toBe("true"),
			);
			expect(tile("Events").textContent).toMatch(/950/);
			expect(tile("Events").textContent).not.toMatch(/\b10\b/);
		});

		// The other half of the same regression: "New users" counts event types,
		// and a category drill-down can only subtract from that set, never refine
		// it. Counting off the category-sliced aggregate made the tile read 0
		// here — the registrations are CategoryIdentity, the filter is security.
		it("keeps the New users count whole under a category filter", async () => {
			serveAudit();
			renderInApp(<AuditPage />);

			await waitFor(() => expect(tile("New users").textContent).toMatch(/40/));

			fireEvent.click(
				await screen.findByRole("button", { name: /Security events/ }),
			);

			await waitFor(() =>
				expect(
					screen
						.getByRole("button", { name: /Security events/ })
						.getAttribute("aria-pressed"),
				).toBe("true"),
			);
			expect(tile("New users").textContent).toMatch(/40/);
		});

		it("the Events tile resets every filter back to unfiltered", async () => {
			const queryAuditLog = vi.fn();
			serveAudit({ queryAuditLog });
			renderInApp(<AuditPage />);

			const security = await screen.findByRole("button", {
				name: /Security events/,
			});
			fireEvent.click(security);
			await waitFor(() =>
				expect(queryAuditLog.mock.calls.at(-1)?.[0]?.category).toBe("security"),
			);

			const events = screen.getByRole("button", { name: /^Events/ });
			fireEvent.click(events);

			await waitFor(() =>
				expect(queryAuditLog.mock.calls.at(-1)?.[0]?.category).toBeFalsy(),
			);
			expect(events.getAttribute("aria-pressed")).toBe("true");
			expect(security.getAttribute("aria-pressed")).toBe("false");
		});

		it("the New users and Active actors tiles stay static (no matching single filter value exists)", async () => {
			serveAudit();
			renderInApp(<AuditPage />);
			await screen.findByText("New users");
			expect(screen.queryByRole("button", { name: /New users/ })).toBeNull();
			expect(
				screen.queryByRole("button", { name: /Active actors/ }),
			).toBeNull();
		});

		// The download is the thing on screen. Before this, it carried only the
		// event type — so a viewer looking at 10 security events in the last 30
		// days downloaded every category, for all of history, with nothing in
		// the file saying so.
		it("exports the filters and the window the page is showing", async () => {
			const queryAuditLog = vi.fn();
			serveAudit({ queryAuditLog });
			renderInApp(<AuditPage />);

			fireEvent.click(
				await screen.findByRole("button", { name: /Security events/ }),
			);
			await waitFor(() =>
				expect(queryAuditLog.mock.calls.at(-1)?.[0]?.category).toBe("security"),
			);

			fireEvent.click(screen.getByRole("button", { name: /Export/ }));
			fireEvent.click(await screen.findByText("Export as CSV"));

			await waitFor(() =>
				expect(queryAuditLog.mock.calls.at(-1)?.[0]?.pageSize).toBe(10000),
			);
			const exported = queryAuditLog.mock.calls.at(-1)?.[0];
			expect(exported?.category).toBe("security");
			expect(exported?.from).toBeTruthy();
			expect(exported?.to).toBeTruthy();
		});
	});
});
