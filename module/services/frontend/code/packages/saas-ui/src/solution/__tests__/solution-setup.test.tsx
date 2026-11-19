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
const role = (name: string) =>
	within(screen.getByRole("region", { name: `Role ${name}` }));
const openDetails = () =>
	fireEvent.click(screen.getByRole("button", { name: "Technical details" }));

async function mounted(fixture = setupFixture(), extra = {}) {
	render(
		<SolutionSetup
			transport={fixture.transport}
			organizationId={exampleOrganization}
			target={exampleTarget}
			{...extra}
		/>,
	);
	await screen.findByRole("heading", { name: "Status" });
	return fixture;
}

describe("Status", () => {
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
		expect(screen.queryByRole("heading", { name: "Status" })).toBeNull();
		await act(() => vi.advanceTimersByTimeAsync(300));
		expect(screen.queryByRole("status")).toBeNull();
		expect(screen.getByRole("heading", { name: "Status" })).toBeTruthy();
	});

	it("says in one line that it is installed, ready, and what the host adds", async () => {
		await mounted(setupFixture(), {
			conditions: [],
			facts: [{ text: "Healthy", tone: "success" }],
		});
		const line = within(screen.getByLabelText("Status"));
		expect(line.getByText("Installed")).toBeTruthy();
		expect(line.getByText("Ready")).toBeTruthy();
		expect(line.getByText("Healthy")).toBeTruthy();
		// The identifiers are an engineer's, behind the disclosure.
		expect(screen.queryByText("Installation revision")).toBeNull();
		openDetails();
		expect(screen.getByText("Installation revision")).toBeTruthy();
		expect(screen.getByText("4")).toBeTruthy();
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
		expect(
			within(screen.getByLabelText("Status")).getByText("1 thing to fix"),
		).toBeTruthy();
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

	it("says a solution not installed here gives nobody a role yet, and offers no install", async () => {
		await mounted(
			setupFixture({ offer: reviewedOffer({ tools: true, installed: false }) }),
		);
		expect(
			within(screen.getByLabelText("Status")).getByText("Not installed"),
		).toBeTruthy();
		expect(screen.getByText(/Not installed in your organization/)).toBeTruthy();
		expect(screen.queryByRole("button", { name: /Install/ })).toBeNull();
		expect(
			role("user").getByText("Nobody, until it is installed."),
		).toBeTruthy();
	});

	it("names what an update asks beyond what was approved, with the host's control", async () => {
		await mounted(
			setupFixture({
				offer: reviewedOffer({ tools: false, upgradeAvailable: true }),
			}),
			{
				update: {
					asks: [
						{
							role: "admin",
							groups: [
								{
									key: "b",
									where: "Through example-pages and assistant, in documents",
									lines: ["create collections"],
								},
							],
						},
					],
					action: <button type="button">Review the update</button>,
				},
			},
		);
		const update = within(
			screen.getByText("Update available").closest("[role]") as HTMLElement,
		);
		expect(update.getByText("An admin would also be able to")).toBeTruthy();
		expect(update.getByText("Create collections")).toBeTruthy();
		expect(
			update.getByRole("button", { name: "Review the update" }),
		).toBeTruthy();
	});

	it("reads each role as approved while an update waits, never as the release under review", async () => {
		await mounted(
			setupFixture({
				offer: reviewedOffer({ tools: false, upgradeAvailable: true }),
			}),
			{
				update: {
					approved: [
						{
							name: "admin",
							scopes: [],
							reach: [
								{
									path: [exampleTarget],
									module: exampleTarget,
									via: "role",
									confinement: "installation",
									scopes: [
										{ resourceKind: "example-pages.page", actions: ["read"] },
									],
									operations: [],
								},
							],
						},
					],
				},
			},
		);
		const admin = role("admin");
		expect(admin.getByText("Read pages")).toBeTruthy();
		expect(admin.queryByText("Read and edit documents")).toBeNull();
		expect(screen.getByText(/as you approved it/)).toBeTruthy();
	});

	it("says an update is available even when the host has not read what it asks", async () => {
		await mounted(
			setupFixture({
				offer: reviewedOffer({ tools: false, upgradeAvailable: true }),
			}),
		);
		expect(
			screen.getByText(/A newer release asks for more than you approved/),
		).toBeTruthy();
	});

	it("says in words what the authority reports unreached, and keeps its own words in the details", async () => {
		const condition =
			"role user reaches nothing of model.profile:invoke through assistant: no module assistant reaches consumes it for a person";
		await mounted(
			setupFixture({
				offer: reviewedOffer({ tools: false, unreached: [condition] }),
			}),
		);
		const unreached = within(
			screen.getByRole("list", { name: "Unreached access" }),
		);
		expect(
			unreached.getByText(
				"A user can't use profiles through assistant right now: assistant no longer passes it on.",
			),
		).toBeTruthy();
		expect(screen.queryByText(condition)).toBeNull();
		openDetails();
		expect(screen.getByText(condition)).toBeTruthy();
	});
});

describe("Roles", () => {
	it("says what each role can do, grouped by where, through every hop", async () => {
		await mounted(setupFixture({ offer: reviewedOffer({ tools: true }) }));
		const user = within(
			role("user").getByRole("list", { name: "What a user can do" }),
		);
		expect(user.getByText(`In ${exampleTarget}`)).toBeTruthy();
		expect(user.getByText("Read pages")).toBeTruthy();
		expect(
			user.getByText(`Through ${exampleTarget} and assistant, in model`),
		).toBeTruthy();
		expect(user.getByText("Use profiles")).toBeTruthy();
		expect(user.getByText("With their own access in directory")).toBeTruthy();
		const admin = role("admin");
		expect(admin.getByText("Read and edit pages")).toBeTruthy();
		expect(admin.getByText("Read and edit documents")).toBeTruthy();
		// No operation count, no wire path, no ceiling: those are details.
		expect(screen.queryByText(/operations ·/)).toBeNull();
		expect(screen.queryByText("Permission ceiling")).toBeNull();
		expect(screen.queryByText(/Agent tools cannot be shown/)).toBeNull();
	});

	it("keeps the operations, tools and ceiling behind the technical details", async () => {
		await mounted(setupFixture({ offer: reviewedOffer({ tools: true }) }));
		openDetails();
		fireEvent.click(
			screen.getByRole("button", {
				name: `${exampleTarget} · 2 operations · 1 read, 1 write`,
			}),
		);
		const table = within(
			screen.getByRole("table", { name: `Operations of ${exampleTarget}` }),
		);
		expect(table.getByText("POST /items/{id}")).toBeTruthy();
		expect(table.getByText("write_item · changes data")).toBeTruthy();
		const write = table.getByText("POST /items/{id}").closest("tr");
		const cells = [...(write?.querySelectorAll("td") ?? [])].map(
			(cell) => cell.textContent,
		);
		expect(cells.slice(2, 4)).toEqual(["Yes", "—"]);
		const ceiling = within(
			screen.getByRole("list", { name: "Permission ceiling" }),
		);
		expect(ceiling.getByText("documents")).toBeTruthy();
		expect(ceiling.getByText("example-records: read, write")).toBeTruthy();
	});

	it("says in the details, once and plainly, that a host reading no tool bindings lists no tool", async () => {
		await mounted(setupFixture({ offer: reviewedOffer({ tools: false }) }));
		openDetails();
		expect(
			screen.getByText(
				"Agent tools are not listed: this host does not read them.",
			),
		).toBeTruthy();
	});

	it("lists a module the declaration depends on in the ceiling, as reached by the person", async () => {
		const offer = reviewedOffer({
			tools: false,
			edgeDependencies: [
				{ module: "example-records", presence: "required" },
				{ module: "documents", presence: "optional" },
			],
		});
		await mounted(setupFixture({ offer }));
		openDetails();
		const ceiling = within(
			screen.getByRole("list", { name: "Permission ceiling" }),
		);
		const records = ceiling.getByText("example-records").closest("li");
		expect(records?.textContent).toContain(
			"Reached as the person using the solution",
		);
		expect(records?.textContent).toContain(
			"Required: the solution does not work without it.",
		);
		expect(ceiling.getAllByText("documents")).toHaveLength(1);
	});

	it("shows a member holding a role what it lets them do, without the consent tuple, read-only", async () => {
		await mounted(
			setupFixture({
				offer: reviewedOffer({ tools: true, administrator: false }),
				refuse: { grants: changeRefusal },
			}),
		);
		expect(
			within(screen.getByLabelText("Status")).getByText("Installed"),
		).toBeTruthy();
		expect(screen.getByText("Read and edit pages")).toBeTruthy();
		expect(
			screen.getAllByText(
				/Ask an organization administrator to grant access/,
			)[0],
		).toBeTruthy();
		expect(screen.queryByRole("button", { name: /Give .* access/ })).toBeNull();
	});

	it("shows anyone else the authority's one reason in every section", async () => {
		await mounted(setupFixture({ refuse: { offer: outsiderRefusal } }));
		for (const name of ["Status", "Roles"]) {
			expect(
				within(section(name)).getByText(
					/Ask an organization administrator to grant access/,
				),
			).toBeTruthy();
		}
		expect(screen.queryByRole("button", { name: /Give .* access/ })).toBeNull();
		expect(
			screen.queryByRole("button", { name: "Technical details" }),
		).toBeNull();
	});
});

describe("Who has each role", () => {
	const subjects = [
		{ kind: "person" as const, id: "person-jane", label: "Jane Doe" },
		{
			kind: "team" as const,
			id: "team-finance",
			label: "Finance",
			detail: "3 people",
			href: "/admin/teams/team-finance",
		},
	];

	it("names each role's holders beside it, a team with its people and link", async () => {
		await mounted(setupFixture(), {
			subjects,
			teamControls: (team: { label: string }) => (
				<span>People of {team.label}</span>
			),
		});
		const users = within(
			role("user").getByRole("list", { name: "Who has user" }),
		);
		expect(
			users.getByRole("link", { name: "Team Finance" }).getAttribute("href"),
		).toBe("/admin/teams/team-finance");
		expect(users.getByText("· 3 people")).toBeTruthy();
		expect(users.getByText("People of Finance")).toBeTruthy();
		expect(
			within(
				role("admin").getByRole("list", { name: "Who has admin" }),
			).getByText("Jane Doe"),
		).toBeTruthy();
	});

	it("gives a role to a team from the role itself, and the list follows the authority", async () => {
		const fixture = await mounted(setupFixture({ grants: [] }), { subjects });
		const admin = role("admin");
		expect(admin.getByText("Nobody has it yet.")).toBeTruthy();
		fireEvent.click(admin.getByLabelText("Person or team"));
		const team = await screen.findByRole("option", { name: "Team Finance" });
		fireEvent.pointerDown(team, { pointerType: "mouse" });
		fireEvent.click(team);
		fireEvent.click(admin.getByRole("button", { name: "Give admin access" }));
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(fixture.calls[0]).toMatchObject({
			method: "GrantRole",
			request: {
				organizationId: "org-acme",
				installationId: "installation-1",
				role: "admin",
				teamId: "team-finance",
				personId: "",
			},
		});
		expect(
			await role("admin").findByRole("link", { name: "Team Finance" }),
		).toBeTruthy();
	});

	it("gives a role to an id when the host names nobody", async () => {
		const fixture = await mounted(setupFixture({ grants: [] }));
		const user = role("user");
		fireEvent.change(user.getByLabelText("Person id"), {
			target: { value: "person-omar" },
		});
		fireEvent.click(user.getByRole("button", { name: "Give user access" }));
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(fixture.calls[0].request).toMatchObject({
			role: "user",
			personId: "person-omar",
			teamId: "",
		});
	});

	it("removes one holder and shows the authority's refusal when it refuses", async () => {
		const fixture = await mounted(
			setupFixture({ refuse: { change: changeRefusal } }),
			{ subjects },
		);
		fireEvent.click(
			screen.getByRole("button", { name: "Remove admin from Jane Doe" }),
		);
		// Jane is the admin's only holder: it asks before leaving nobody.
		expect(fixture.calls).toHaveLength(0);
		expect(
			screen.getByText(/Nobody will have admin access to example-pages\./),
		).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Remove access" }));
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(fixture.calls[0]).toMatchObject({
			method: "RevokeRole",
			request: { role: "admin", personId: "person-jane", teamId: "" },
		});
		expect(await screen.findByText("Couldn’t change access")).toBeTruthy();
		expect(
			screen.getByText(/Ask an organization administrator to grant access/),
		).toBeTruthy();
	});

	it("confirms every removal, and keeps the last holder when cancelled", async () => {
		const fixture = await mounted(
			setupFixture({
				grants: [
					{ role: "user", personId: "", teamId: "team-finance" },
					{ role: "user", personId: "person-jane", teamId: "" },
					{ role: "admin", personId: "person-jane", teamId: "" },
				],
			}),
			{ subjects },
		);
		fireEvent.click(
			screen.getByRole("button", { name: "Remove user from Jane Doe" }),
		);
		expect(fixture.calls).toHaveLength(0);
		fireEvent.click(screen.getByRole("button", { name: "Remove access" }));
		await waitFor(() => expect(fixture.calls).toHaveLength(1));
		expect(screen.queryByText(/Nobody will have user access/)).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: "Remove admin from Jane Doe" }),
		);
		fireEvent.click(screen.getByRole("button", { name: "Keep access" }));
		expect(fixture.calls).toHaveLength(1);
		expect(
			screen.getByRole("button", { name: "Remove admin from Jane Doe" }),
		).toBeTruthy();
	});

	it("shows the reason when the holders cannot be read", async () => {
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
			role("user").getByText(
				"The service is temporarily unavailable. Try again in a moment.",
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
			(
				await screen.findAllByText(
					/Ask an organization administrator to grant access/,
				)
			).length,
		).toBeGreaterThan(0);
		expect(seen[0].url).toBe(
			"http://host.test/api/solutions/example/proxy/host.installation.v1.OfferService/ReviewOffer",
		);
		expect(seen[0].authorization).toBe("Bearer token-1");
	});
});
