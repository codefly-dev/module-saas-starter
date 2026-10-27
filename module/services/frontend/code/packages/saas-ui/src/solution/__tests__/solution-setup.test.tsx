import { Code, ConnectError } from "@connectrpc/connect";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	changeRefusal,
	exampleOrganization,
	exampleTarget,
	outsiderRefusal,
	reviewedOffer,
	setupFixture,
} from "../../../stories/setup-fixtures.js";
import { SolutionSetup } from "../index.js";

afterEach(() => {
	cleanup();
	vi.useRealTimers();
});

const section = (name: string) =>
	screen
		.getByRole("heading", { name })
		.closest("[data-slot='card']") as HTMLElement;

async function mounted(fixture = setupFixture(), extra = {}) {
	render(
		<SolutionSetup
			transport={fixture.transport}
			organizationId={exampleOrganization}
			target={exampleTarget}
			{...extra}
		/>,
	);
	await screen.findByRole("heading", { name: "Is it ready?" });
	return fixture;
}

describe("Is it ready?", () => {
	it("keeps an already visible loading indicator for the kit's minimum duration", async () => {
		vi.useFakeTimers();
		const fixture = setupFixture();
		let finish!: () => void;
		const pending = new Promise<void>((resolve) => {
			finish = resolve;
		});
		const transport = {
			...fixture.transport,
			unary: (async (...args: Parameters<typeof fixture.transport.unary>) => {
				await pending;
				return fixture.transport.unary(...args);
			}) as typeof fixture.transport.unary,
		};
		render(
			<SolutionSetup
				transport={transport}
				organizationId={exampleOrganization}
				target={exampleTarget}
			/>,
		);
		expect(screen.queryByRole("status")).toBeNull();
		await act(() => vi.advanceTimersByTimeAsync(200));
		expect(screen.getByRole("status")).toBeTruthy();
		await act(async () => {
			finish();
		});
		expect(screen.getByRole("status")).toBeTruthy();
		expect(screen.queryByRole("heading", { name: "Is it ready?" })).toBeNull();
		await act(() => vi.advanceTimersByTimeAsync(300));
		expect(screen.queryByRole("status")).toBeNull();
		expect(screen.getByRole("heading", { name: "Is it ready?" })).toBeTruthy();
	});

	it("states the installation as the authority holds it", async () => {
		await mounted();
		const ready = within(section("Is it ready?"));
		expect(ready.getByText("Installed")).toBeTruthy();
		expect(ready.getByText("3")).toBeTruthy(); // the reviewed generation
		expect(ready.getByText("4")).toBeTruthy(); // the installation's revision
		expect(ready.getByText("viewer")).toBeTruthy(); // the agent role
	});

	it("lists what the host knows is still unmet, with who clears it", async () => {
		await mounted(setupFixture(), {
			conditions: [
				{
					key: "source:repo",
					heading: "A source is not connected",
					subject: "example/repo",
					detail: "The solution reads a repository nobody connected.",
					clearedBy: "An organization administrator",
					action: {
						kind: "link",
						label: "Connect it",
						href: "/admin/datasources",
					},
				},
			],
		});
		const unmet = screen.getByRole("list", { name: "Unmet conditions" });
		expect(within(unmet).getByText("A source is not connected")).toBeTruthy();
		expect(
			within(unmet).getByText("Cleared by: An organization administrator"),
		).toBeTruthy();
		expect(
			within(unmet)
				.getByRole("link", { name: "Connect it" })
				.getAttribute("href"),
		).toBe("/admin/datasources");
	});

	it("says a solution not installed here holds no role yet, and offers no install", async () => {
		await mounted(
			setupFixture({ offer: reviewedOffer({ tools: true, installed: false }) }),
		);
		expect(
			within(section("Is it ready?")).getByText("Not installed"),
		).toBeTruthy();
		expect(screen.getByText(/Not installed at this organization/)).toBeTruthy();
		expect(screen.queryByRole("button", { name: /Install/ })).toBeNull();
		expect(
			within(section("Who holds them")).getByText(
				/Nobody holds a role until the solution is installed/,
			),
		).toBeTruthy();
	});
});

describe("Roles and what they grant", () => {
	it("lists each role's scopes, operations and tools, and the ceiling (a local-authority answer)", async () => {
		await mounted(setupFixture({ offer: reviewedOffer({ tools: true }) }));
		const roles = within(section("Roles and what they grant"));
		expect(roles.getByRole("heading", { name: "Role: editor" })).toBeTruthy();
		expect(
			roles.getByText(
				"example-pages.page: read, write · what this installation owns",
			),
		).toBeTruthy();
		const editor = roles.getByRole("table", { name: "What editor reaches" });
		expect(within(editor).getByText("POST /items/{id}")).toBeTruthy();
		expect(within(editor).getByText("write_item · changes data")).toBeTruthy();
		const ceiling = roles.getByRole("list", { name: "Permission ceiling" });
		expect(within(ceiling).getByText("documents")).toBeTruthy();
		expect(
			within(ceiling).getByText("example-records: read, write"),
		).toBeTruthy();
		expect(roles.queryByText(/Tool exposure is unavailable/)).toBeNull();
	});

	it("renders a host that composes no tools from the same message, saying so (a SaaS answer)", async () => {
		await mounted(setupFixture({ offer: reviewedOffer({ tools: false }) }));
		const roles = within(section("Roles and what they grant"));
		expect(
			roles.getByText("Tool exposure is unavailable from this host."),
		).toBeTruthy();
		const editor = roles.getByRole("table", { name: "What editor reaches" });
		expect(within(editor).getByText("POST /items/{id}")).toBeTruthy();
		expect(
			within(editor).getAllByText("Tool exposure unavailable"),
		).toHaveLength(2);
	});

	it("shows a member holding a role what it grants, without the consent tuple and read-only", async () => {
		await mounted(
			setupFixture({
				offer: reviewedOffer({ tools: true, administrator: false }),
				refuse: { grants: changeRefusal },
			}),
		);
		const ready = within(section("Is it ready?"));
		expect(ready.getByText("Installed")).toBeTruthy();
		expect(ready.getByText("—", { selector: "dd" })).toBeTruthy(); // no generation without the tuple
		expect(
			within(section("Roles and what they grant")).getByRole("heading", {
				name: "Role: editor",
			}),
		).toBeTruthy();
		const holders = within(section("Who holds them"));
		expect(
			holders.getByText(
				/Refused: installation administration requires an administrator/,
			),
		).toBeTruthy();
		expect(screen.queryByRole("button", { name: "Grant role" })).toBeNull();
	});

	it("shows anyone else the authority's one reason in every section", async () => {
		await mounted(setupFixture({ refuse: { offer: outsiderRefusal } }));
		for (const name of [
			"Is it ready?",
			"Roles and what they grant",
			"Who holds them",
		]) {
			expect(
				within(section(name)).getByText(
					/Refused: reviewing "example-pages" requires an administrator or owner of organization "org-acme", or a role on its installation there/,
				),
			).toBeTruthy();
		}
		expect(screen.queryByRole("button", { name: "Grant role" })).toBeNull();
	});
});

describe("Who holds them", () => {
	const subjects = [
		{ kind: "person" as const, id: "person-jane", label: "Jane Doe" },
		{ kind: "team" as const, id: "team-finance", label: "Finance" },
	];

	it("names each holder as the host names them", async () => {
		await mounted(setupFixture(), { subjects });
		const grants = screen.getByRole("list", { name: "Role grants" });
		expect(within(grants).getByText("viewer · Team Finance")).toBeTruthy();
		expect(within(grants).getByText("editor · Jane Doe")).toBeTruthy();
	});

	it("grants a declared role to a team, and the list follows the authority", async () => {
		const fixture = await mounted(setupFixture({ grants: [] }), { subjects });
		fireEvent.change(screen.getByLabelText("Role"), {
			target: { value: "editor" },
		});
		fireEvent.change(screen.getByLabelText("Person or team"), {
			target: { value: "team:team-finance" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Grant role" }));
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(fixture.calls[0]).toMatchObject({
			method: "GrantRole",
			request: {
				organizationId: "org-acme",
				installationId: "installation-1",
				role: "editor",
				teamId: "team-finance",
				personId: "",
			},
		});
		expect(await screen.findByText("editor · Team Finance")).toBeTruthy();
	});

	it("grants to an id when the host names nobody", async () => {
		const fixture = await mounted(setupFixture({ grants: [] }));
		fireEvent.change(screen.getByLabelText("Role"), {
			target: { value: "viewer" },
		});
		fireEvent.change(screen.getByLabelText("Person id"), {
			target: { value: "person-omar" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Grant role" }));
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(fixture.calls[0].request).toMatchObject({
			role: "viewer",
			personId: "person-omar",
			teamId: "",
		});
	});

	it("revokes one grant and shows the authority's refusal when it refuses", async () => {
		const fixture = await mounted(
			setupFixture({ refuse: { change: changeRefusal } }),
			{ subjects },
		);
		fireEvent.click(
			screen.getByRole("button", { name: "Revoke editor from Jane Doe" }),
		);
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(fixture.calls[0]).toMatchObject({
			method: "RevokeRole",
			request: { role: "editor", personId: "person-jane", teamId: "" },
		});
		const banner = await screen.findByText("The authority refused the change");
		expect(banner).toBeTruthy();
		expect(
			screen.getByText(/Refused: installation administration requires/),
		).toBeTruthy();
	});

	it("shows the reason when the grants cannot be read", async () => {
		await mounted(
			setupFixture({
				refuse: {
					grants: new ConnectError(
						"the store is unreachable",
						Code.Unavailable,
					),
				},
			}),
		);
		expect(
			within(section("Who holds them")).getByText(
				"Unavailable: the store is unreachable",
			),
		).toBeTruthy();
	});
});

describe("a binding instead of a transport", () => {
	it("reaches the host interfaces at the binding's base with its bearer", async () => {
		const seen: { url: string; authorization: string | null }[] = [];
		const authedFetch = async (
			input: RequestInfo | URL,
			init?: RequestInit,
		) => {
			const request = new Request(input, init);
			seen.push({
				url: request.url,
				authorization: request.headers.get("authorization"),
			});
			return new Response(
				JSON.stringify({ code: "permission_denied", message: "not here" }),
				{
					status: 403,
					headers: { "content-type": "application/json" },
				},
			);
		};
		render(
			<SolutionSetup
				binding={{
					apiBase: "http://host.test/api/solutions/example/proxy",
					getAccessToken: () => "token-1",
					authedFetch,
				}}
				organizationId={exampleOrganization}
				target={exampleTarget}
			/>,
		);
		expect(
			(await screen.findAllByText("Refused: not here")).length,
		).toBeGreaterThan(0);
		expect(seen[0].url).toBe(
			"http://host.test/api/solutions/example/proxy/host.installation.v1.OfferService/ReviewOffer",
		);
		expect(seen[0].authorization).toBe("Bearer token-1");
	});
});
