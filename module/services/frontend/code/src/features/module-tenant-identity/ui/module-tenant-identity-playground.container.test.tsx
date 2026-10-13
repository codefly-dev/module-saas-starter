import {
	act,
	cleanup,
	fireEvent,
	screen,
	within,
} from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ModuleTenantIdentityPlayground } from "./module-tenant-identity-playground";

// THE PAGE RESOLVES A MINT AND NEVER PERFORMS ONE.
//
// That is the claim worth testing, and it is a claim about what the page asks
// for rather than about what renders: a surface that answered an operator with
// a module's capability would be a second spelling of that module's identity.
// So the test records the requests the page makes, asserts they are the
// resolution and nothing else, and then asserts the two answers an operator
// has to tell apart — what would be carried, and why nothing would be.

afterEach(cleanup);

const ORG = "11111111-1111-4111-8111-111111111111";

const minted = {
	identity: {
		principalId: "module:example",
		tenant: ORG,
		audience: "example.api",
		scopes: ["records:read:one", "records:write:one"],
		lifetimeSeconds: "60",
		revision: "8123456789",
		shared: true,
	},
	refusal: "",
	recent: [
		{
			record: "audit-1",
			tenant: ORG,
			binding: "operate",
			audience: "example.api",
			scopes: ["records:read:one"],
			mintedAt: "2026-10-13T09:00:00Z",
		},
	],
};

const refused = {
	refusal: "the organization has no live installation of this module",
	recent: [],
};

/** Serve the resolution, and record every request the page makes. */
function serve(answer: JsonBodyType): { calls: unknown[] } {
	const calls: unknown[] = [];
	server.use(
		http.post(
			rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
			async ({ request }) => {
				calls.push(await request.json());
				return HttpResponse.json(answer);
			},
		),
	);
	return { calls };
}

function fill() {
	fireEvent.change(screen.getByLabelText("Module prefix"), {
		target: { value: "example" },
	});
	fireEvent.change(screen.getByLabelText("Operation binding"), {
		target: { value: "operate" },
	});
	fireEvent.change(screen.getByLabelText("Organization"), {
		target: { value: ORG },
	});
}

describe("ModuleTenantIdentityPlayground", () => {
	it("asks nothing until the triple is named", async () => {
		const recorded = serve(minted);
		renderInApp(<ModuleTenantIdentityPlayground />);

		// The answer is about the three together, so two of them is a different
		// question — and showing its answer as this one's would be wrong.
		fireEvent.change(screen.getByLabelText("Module prefix"), {
			target: { value: "example" },
		});
		fireEvent.change(screen.getByLabelText("Operation binding"), {
			target: { value: "operate" },
		});
		expect(
			await screen.findByText(/Name a module, a binding and an organization/),
		).toBeTruthy();
		expect(recorded.calls).toHaveLength(0);
	});

	it("shows what one tenant's capability would carry, and asks for no mint", async () => {
		const recorded = serve(minted);
		renderInApp(<ModuleTenantIdentityPlayground />);
		fill();

		expect(
			await screen.findByText(/A capability would be issued/),
		).toBeTruthy();
		// Acting FOR the organization named: the tenant is the capability's, and
		// it is what a consumer admitting a publication reads it from.
		// Scoped to the resolution's own card: the audience also appears in the
		// trail below it, and `getByText` over the page would be ambiguous for
		// the wrong reason.
		const resolved = document.querySelector<HTMLElement>(
			'[data-slot="module-tenant-identity"]',
		) as HTMLElement;
		expect(within(resolved).getByText(ORG)).toBeTruthy();
		expect(within(resolved).getByText("example.api")).toBeTruthy();
		expect(within(resolved).getByText("60 seconds")).toBeTruthy();
		// The scopes are the binding's headless list as this organization's
		// installation narrowed it — never its invoke scopes.
		expect(
			within(resolved).getByText("records:read:one, records:write:one"),
		).toBeTruthy();
		expect(
			screen.getByText(/may act for any organization that installs it/),
		).toBeTruthy();

		// The trail of capabilities actually issued, which is the only record of
		// one: nothing caches a capability.
		expect(
			document.querySelectorAll('[data-slot="module-tenant-mint"]'),
		).toHaveLength(1);

		// And the whole point: one request, the resolution, carrying the triple
		// — and no mint anywhere.
		expect(recorded.calls).toEqual([
			{ prefix: "example", binding: "operate", organizationId: ORG },
		]);
	});

	it("shows the refusal in the host's own words", async () => {
		// A refusal is a decision about declared authority, and each one is a
		// different thing for an operator to do about it — so the page prints
		// what the host said rather than mapping every refusal onto one word.
		serve(refused);
		renderInApp(<ModuleTenantIdentityPlayground />);
		fill();

		expect(
			await screen.findByText(/No capability would be issued/),
		).toBeTruthy();
		expect(
			screen.getByText(/has no live installation of this module/),
		).toBeTruthy();
		expect(
			screen.getByText(/is a decision about declared authority, not an outage/),
		).toBeTruthy();
		// A refused resolution shows no capability's facts at all.
		expect(
			document.querySelector('[data-slot="module-tenant-identity"]'),
		).toBeNull();
	});
});

describe("independent review: authority read outcomes", () => {
	it("does not claim no issued capabilities before a read", () => {
		renderInApp(<ModuleTenantIdentityPlayground />);
		expect(screen.queryByText(/No capability has been issued/)).toBeNull();
	});
	it("removes issued capability rows after a forbidden refetch", async () => {
		serve(minted);
		renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				() =>
					HttpResponse.json(
						{
							code: "permission_denied",
							message: "requires platform super_admin role",
						},
						{ status: 403 },
					),
			),
		);
		fireEvent.click(screen.getByRole("button", { name: "Resolve again" }));
		await screen.findAllByText(/not allowed|permission|cannot view|access/i);
		expect(screen.queryByText("audit-1")).toBeNull();
	});
	it("does not restore a forbidden snapshot after a later unavailable read", async () => {
		serve(minted);
		renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				() =>
					HttpResponse.json(
						{ code: "permission_denied", message: "refused" },
						{ status: 403 },
					),
			),
		);
		fireEvent.click(screen.getByRole("button", { name: "Resolve again" }));
		await screen.findAllByText(/don't have permission/);
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				() =>
					HttpResponse.json(
						{ code: "unavailable", message: "unavailable" },
						{ status: 503 },
					),
			),
		);
		fireEvent.click(screen.getByRole("button", { name: "Resolve again" }));
		await screen.findAllByText(/Couldn't load/);
		expect(screen.queryByText("audit-1")).toBeNull();
	});
	it("retains a successful answer with a notice on transient failure", async () => {
		serve(minted);
		renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				() =>
					HttpResponse.json(
						{ code: "unavailable", message: "unavailable" },
						{ status: 503 },
					),
			),
		);
		fireEvent.click(screen.getByRole("button", { name: "Resolve again" }));
		await screen.findByText(/may be out of date/);
		expect(screen.getByText("audit-1")).toBeTruthy();
	});
});

// AND THE REFUSAL IS ABOUT ONE TRIPLE (issue #1121, the same scope finding the
// declaration query carried). `findAll({ queryKey })` matches by prefix, so a
// refusal here deleted every cached triple — other bindings, other
// organizations — none of which had been refused. This page asks one triple at
// a time, so the harm was a needless refetch rather than a false claim; the rule
// is the same either way, and this pins it.
describe("a refusal's blast radius is the triple it answered", () => {
	it("keeps another organization's cached answer", async () => {
		serve(minted);
		const { client } = renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");

		const other = [
			"module-tenant-identity",
			"saas",
			"module:audit",
			"22222222-2222-4222-8222-222222222222",
		] as const;
		client.setQueryData(other, minted);

		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				() =>
					HttpResponse.json(
						{ code: "permission_denied", message: "refused" },
						{ status: 403 },
					),
			),
		);
		fireEvent.click(screen.getByRole("button", { name: "Resolve again" }));
		await screen.findAllByText(/not allowed|permission|cannot view|access/i);

		expect(screen.queryByText("audit-1")).toBeNull();
		expect(client.getQueryData(other)).toBeTruthy();
	});
});

// AND THE SAME SUPERSESSION RACE, in the other query family (issue #1121).
//
// Two fetches overlap on one triple. The older is superseded, answers last and
// answers 403, and its catch deleted the answer the NEWER fetch had written —
// leaving status `success`, fetchStatus `idle`, `data` undefined and `error`
// null, which no component can read correctly. The guard is the same: delete
// only when no newer success replaced what this fetch was reading.
describe("a superseded refusal in the identity read", () => {
	const KEY = ["module-tenant-identity", "example", "operate", ORG] as const;

	it("does not erase the newer answer it was superseded by", async () => {
		let call = 0;
		let release403: (() => void) | undefined;
		const held = new Promise<void>((resolve) => {
			release403 = resolve;
		});
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				async () => {
					call++;
					if (call === 1) return HttpResponse.json(minted);
					if (call === 2) {
						await held;
						return HttpResponse.json(
							{ code: "permission_denied", message: "refused" },
							{ status: 403 },
						);
					}
					return HttpResponse.json(minted);
				},
			),
		);
		const { client } = renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");

		const stale = client
			.refetchQueries({ queryKey: KEY, exact: true })
			.catch(() => undefined);
		await act(async () => {
			await client.refetchQueries({ queryKey: KEY, exact: true });
		});

		release403?.();
		await act(async () => {
			await stale;
		});

		expect(client.getQueryState([...KEY])?.status).toBe("success");
		expect(client.getQueryData([...KEY])).toBeTruthy();
		expect(screen.getByText("audit-1")).toBeTruthy();
	});

	// The deletion still happens when nothing newer replaced it, unmounted.
	it("still deletes a refusal that nothing newer replaced, with the view unmounted", async () => {
		serve(minted);
		const { client } = renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");
		expect(client.getQueryData([...KEY])).toBeTruthy();

		cleanup();
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				() =>
					HttpResponse.json(
						{ code: "permission_denied", message: "refused" },
						{ status: 403 },
					),
			),
		);
		await act(async () => {
			await client
				.refetchQueries({ queryKey: KEY, exact: true, type: "all" })
				.catch(() => undefined);
		});

		expect(client.getQueryData([...KEY])).toBeUndefined();
	});
});

// AND THE SAME FIELD CHOICE HERE (issue #1121, r23): the guard counts
// generations, not milliseconds. `dataUpdatedAt` is a millisecond timestamp, so
// two successes in one millisecond tie — the spacing an overlapping refetch
// actually has — and the superseded refusal would delete the newer answer after
// all. Only Date is faked, so promises and MSW keep working while every
// `Date.now()` returns one instant.
describe("a superseded refusal when both identity answers share a millisecond", () => {
	const KEY = ["module-tenant-identity", "example", "operate", ORG] as const;

	afterEach(() => vi.useRealTimers());

	it("does not erase a newer success that landed in the same millisecond", async () => {
		vi.useFakeTimers({ toFake: ["Date"] });
		vi.setSystemTime(new Date("2026-10-13T12:00:00.000Z"));

		let call = 0;
		let release403: (() => void) | undefined;
		const held = new Promise<void>((resolve) => {
			release403 = resolve;
		});
		server.use(
			http.post(
				rpc("PlatformAdminService", "ResolveModuleTenantIdentity"),
				async () => {
					call++;
					if (call === 1) return HttpResponse.json(minted);
					if (call === 2) {
						await held;
						return HttpResponse.json(
							{ code: "permission_denied", message: "refused" },
							{ status: 403 },
						);
					}
					return HttpResponse.json(minted);
				},
			),
		);
		const { client } = renderInApp(<ModuleTenantIdentityPlayground />);
		fill();
		await screen.findByText("audit-1");

		const stale = client
			.refetchQueries({ queryKey: KEY, exact: true })
			.catch(() => undefined);
		await act(async () => {
			await client.refetchQueries({ queryKey: KEY, exact: true });
		});

		// The premise, asserted: one timestamp, two generations.
		const after = client.getQueryState([...KEY]);
		expect(after?.dataUpdatedAt).toBe(
			new Date("2026-10-13T12:00:00.000Z").getTime(),
		);
		expect(after?.dataUpdateCount).toBeGreaterThan(1);

		release403?.();
		await act(async () => {
			await stale;
		});

		expect(client.getQueryData([...KEY])).toBeTruthy();
		expect(client.getQueryState([...KEY])?.status).toBe("success");
	});
});
