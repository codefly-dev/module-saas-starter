import { create } from "@bufbuild/protobuf";
import { QueryClient } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReviewedOfferSchema } from "@/gen/host/installation/v1/installation_pb";
import {
	InstallationSchema,
	InstallationStatus,
} from "@/gen/saas/accounts/v1/installations_pb";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import {
	acknowledgeTeamChoices,
	useInstallationTeamChoices,
} from "../service/team-choices";
import { InstallationTeamChoices } from "./installation-team-choices";
import { SolutionSetupPanel } from "./solution-setup-panel";

const identity = vi.hoisted(() => ({
	organizationId: "org-1",
	user: { id: "person-1" },
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => identity }));
vi.mock("@/lib/hooks/use-organizations", () => ({
	useOrgMembers: () => ({ data: [] }),
}));
const offer = {
	target: "Example Solution",
	approval: {
		targetId: "signed-principal",
		generation: "2",
		digest: "reviewed-digest",
	},
	roles: [
		{
			name: "editor",
			scopes: [{ resourceKind: "example.page", actions: ["write"] }],
		},
		{ name: "reader" },
	],
};
let active: boolean;
let failGrant: boolean;
let grants: unknown[];
let teams = [{ id: "team-1", name: "Example team" }];
let creates: unknown[];
let lostCreate: boolean;
beforeEach(() => {
	window.sessionStorage.clear();
	identity.organizationId = "org-1";
	active = false;
	failGrant = false;
	grants = [];
	creates = [];
	lostCreate = false;
	teams = [{ id: "team-1", name: "Example team" }];
	server.use(
		http.post(rpc("TeamService", "CreateTeam"), async ({ request }) => {
			const body = (await request.json()) as { name: string };
			creates.push(body);
			const team = { id: "created-team", name: body.name };
			teams.push(team);
			return lostCreate
				? HttpResponse.json(
						{ code: "unavailable", message: "Response lost" },
						{ status: 503 },
					)
				: HttpResponse.json({ team });
		}),
		http.post(rpc("TeamService", "ListTeams"), () =>
			HttpResponse.json({ teams }),
		),
		http.post(
			"http://localhost:3000/host.installation.v1.OfferService/ReviewOffer",
			() =>
				HttpResponse.json({
					...offer,
					installation: {
						id: "installation-1",
						organizationId: "org-1",
						target: offer.target,
						status: active ? "STATUS_ACTIVE" : "STATUS_PENDING",
						revision: "1",
					},
				}),
		),
		http.post(
			"http://localhost:3000/host.installation.v1.InstallationService/ListRoleGrants",
			() => HttpResponse.json({ grants: [] }),
		),
		http.post(
			"http://localhost:3000/host.installation.v1.InstallationService/GrantRole",
			async ({ request }) => {
				grants.push(await request.json());
				return failGrant
					? HttpResponse.json(
							{ code: "unavailable", message: "Try again" },
							{ status: 503 },
						)
					: HttpResponse.json({});
			},
		),
	);
});
afterEach(cleanup);
async function choose() {
	const panel = await screen.findByRole("region", { name: "Team access" });

	const role = within(panel).getByLabelText("Team for editor");
	await waitFor(() =>
		expect(
			(within(role).getByLabelText("Team") as HTMLSelectElement).options.length,
		).toBeGreaterThan(1),
	);
	expect(within(panel).queryByLabelText("Team for admin")).toBeNull();
	fireEvent.change(within(role).getByLabelText("Team"), {
		target: { value: "team:team-1" },
	});
	fireEvent.click(
		within(role).getByRole("button", { name: "Use team for editor" }),
	);
	expect(await within(panel).findByText(/Example team — editor/)).toBeTruthy();
	return panel;
}
const reviewed = () =>
	create(ReviewedOfferSchema, {
		...offer,
		approval: { ...offer.approval, generation: BigInt(2) },
	});
const installation = (status: InstallationStatus) =>
	create(InstallationSchema, {
		id: "installation-1",
		targetId: "target-1",
		status,
		revision: BigInt(1),
	});
describe("reviewed team choices use the existing solution role owner", () => {
	it("retains choices across pending, remount and failed grant; sends no grant until active confirmation", async () => {
		const initial = renderInApp(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={reviewed()}
			/>,
		);
		await choose();
		expect(grants).toHaveLength(0);
		initial.unmount();
		const setup = renderInApp(
			<SolutionSetupPanel
				target={offer.target}
				targetId="target-1"
				installation={installation(InstallationStatus.PENDING)}
			/>,
		);
		expect(await screen.findByText(/Example team — editor/)).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: "Apply team access" }),
		).toBeNull();
		expect(grants).toHaveLength(0);
		active = true;
		failGrant = true;
		setup.rerender(
			<SolutionSetupPanel
				target={offer.target}
				targetId="target-1"
				installation={installation(InstallationStatus.ACTIVE)}
			/>,
		);
		const applyButton = await screen.findByRole("button", {
			name: "Apply team access",
		});
		await waitFor(() =>
			expect((applyButton as HTMLButtonElement).disabled).toBe(false),
		);
		fireEvent.click(applyButton);
		expect(
			await screen.findByText(/Your team choices are retained/),
		).toBeTruthy();
		expect(screen.getByText(/Example team — editor/)).toBeTruthy();
		expect(grants).toEqual([
			{
				organizationId: "org-1",
				installationId: "installation-1",
				teamId: "team-1",
				role: "editor",
			},
		]);
		failGrant = false;
		fireEvent.click(screen.getByRole("button", { name: "Apply team access" }));
		await waitFor(() => expect(grants).toHaveLength(2));
		await waitFor(() =>
			expect(screen.queryByLabelText("Chosen team roles")).toBeNull(),
		);
		expect(grants[1]).toEqual(grants[0]);
	});
	it("requires explicit re-review after declaration change and never invents roles or crosses organizations", async () => {
		const apply = vi.fn(async () => {});
		const view = renderInApp(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={reviewed()}
				grant={apply}
			/>,
		);
		await choose();
		const updated = reviewed();
		updated.approval!.digest = "changed-digest";
		view.rerender(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={updated}
				grant={apply}
			/>,
		);
		expect(
			(
				screen.getByRole("button", {
					name: "Apply team access",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		fireEvent.click(
			screen.getByRole("button", {
				name: "Confirm team choices for current declaration",
			}),
		);
		await waitFor(() =>
			expect(
				(
					screen.getByRole("button", {
						name: "Apply team access",
					}) as HTMLButtonElement
				).disabled,
			).toBe(false),
		);
		view.rerender(
			<InstallationTeamChoices
				orgId="org-2"
				targetId="target-1"
				offer={updated}
				grant={apply}
			/>,
		);
		await waitFor(() =>
			expect(screen.queryByLabelText("Chosen team roles")).toBeNull(),
		);
		expect(apply).not.toHaveBeenCalled();
	});
});

describe("inline organization team creation", () => {
	it("suggests a name for each declared role, allows editing, and retains the created team without members or grants", async () => {
		const view = renderInApp(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={reviewed()}
			/>,
		);
		const button = await screen.findByRole("button", {
			name: "Create team for editor",
		});
		await waitFor(() =>
			expect((button as HTMLButtonElement).disabled).toBe(false),
		);
		fireEvent.click(button);
		const dialog = screen.getByRole("dialog");
		expect(
			(within(dialog).getByLabelText("Name") as HTMLInputElement).value,
		).toBe("example-solution-editor");
		fireEvent.change(within(dialog).getByLabelText("Name"), {
			target: { value: "Example authors" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));
		expect(await screen.findByText(/Example authors — editor/)).toBeTruthy();
		expect(creates).toEqual([{ orgId: "org-1", name: "Example authors" }]);
		expect(grants).toHaveLength(0);
		view.unmount();
		renderInApp(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={reviewed()}
			/>,
		);
		expect(await screen.findByText(/Example authors — editor/)).toBeTruthy();
		expect(creates).toHaveLength(1);
		expect(grants).toHaveLength(0);
	});
	it("offers an existing same-name team explicitly after a lost create response instead of recreating or assigning silently", async () => {
		lostCreate = true;
		renderInApp(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={reviewed()}
			/>,
		);
		const button = await screen.findByRole("button", {
			name: "Create team for reader",
		});
		await waitFor(() =>
			expect((button as HTMLButtonElement).disabled).toBe(false),
		);
		fireEvent.click(button);
		fireEvent.click(
			within(screen.getByRole("dialog")).getByRole("button", {
				name: "Create",
			}),
		);
		expect(await screen.findByText(/Response lost/)).toBeTruthy();
		expect(screen.queryByLabelText("Chosen team roles")).toBeNull();
		fireEvent.click(
			within(screen.getByRole("dialog")).getByRole("button", {
				name: "Create",
			}),
		);
		const reuse = await screen.findByRole("button", {
			name: "Use existing example-solution-reader",
		});
		expect(creates).toHaveLength(1);
		expect(screen.queryByLabelText("Chosen team roles")).toBeNull();
		fireEvent.click(reuse);
		expect(
			await screen.findByText(/example-solution-reader — reader/),
		).toBeTruthy();
		expect(creates).toHaveLength(1);
		expect(grants).toHaveLength(0);
	});
});

function AuthorizeInstall({ operationId }: { operationId: string }) {
	const draft = useInstallationTeamChoices("org-1", "target-1");
	return (
		<button type="button" onClick={() => draft.authorize(operationId)}>
			Install
		</button>
	);
}

describe("choices approved with Install", () => {
	it("grant nothing while setup is pending and are applied once, automatically, after activation", async () => {
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const dialog = renderInApp(
			<>
				<InstallationTeamChoices
					orgId="org-1"
					targetId="target-1"
					offer={reviewed()}
				/>
				<AuthorizeInstall operationId="operation-1" />
			</>,
			client,
		);
		await choose();
		fireEvent.click(screen.getByRole("button", { name: "Install" }));
		// Another operation's answer never binds these choices.
		acknowledgeTeamChoices(
			client,
			"person-1",
			"org-1",
			"target-1",
			"operation-2",
			"installation-1",
		);
		acknowledgeTeamChoices(
			client,
			"person-1",
			"org-1",
			"target-1",
			"operation-1",
			"installation-1",
		);
		dialog.unmount();

		const setup = renderInApp(
			<SolutionSetupPanel
				target={offer.target}
				targetId="target-1"
				installation={installation(InstallationStatus.PENDING)}
			/>,
			client,
		);
		expect(
			await screen.findByText(/granted only after setup is active/),
		).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: "Apply team access" }),
		).toBeNull();
		expect(grants).toHaveLength(0);

		active = true;
		setup.rerender(
			<SolutionSetupPanel
				target={offer.target}
				targetId="target-1"
				installation={installation(InstallationStatus.ACTIVE)}
			/>,
		);
		await waitFor(() => expect(grants).toHaveLength(1));
		expect(grants[0]).toEqual({
			organizationId: "org-1",
			installationId: "installation-1",
			teamId: "team-1",
			role: "editor",
		});
		expect(await screen.findByText("Team access is applied.")).toBeTruthy();
		expect(screen.queryByLabelText("Chosen team roles")).toBeNull();
		expect(grants).toHaveLength(1);
	});
});

describe("a team name that is already taken", () => {
	it("is surfaced instead of creating a duplicate or reusing the team, and an edited name creates a new team", async () => {
		teams = [
			{ id: "team-1", name: "Example team" },
			{ id: "team-2", name: "Example-Solution-Editor" },
		];
		renderInApp(
			<InstallationTeamChoices
				orgId="org-1"
				targetId="target-1"
				offer={reviewed()}
			/>,
		);
		const button = await screen.findByRole("button", {
			name: "Create team for editor",
		});
		await waitFor(() =>
			expect((button as HTMLButtonElement).disabled).toBe(false),
		);
		fireEvent.click(button);
		const dialog = screen.getByRole("dialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));
		expect((await within(dialog).findByRole("alert")).textContent).toMatch(
			/“Example-Solution-Editor” already exists/,
		);
		expect(creates).toHaveLength(0);
		expect(screen.queryByLabelText("Chosen team roles")).toBeNull();
		expect(
			within(dialog).getByRole("button", {
				name: "Use existing Example-Solution-Editor",
			}),
		).toBeTruthy();
		fireEvent.change(within(dialog).getByLabelText("Name"), {
			target: { value: "example-solution-editors" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));
		expect(
			await screen.findByText(/example-solution-editors — editor/),
		).toBeTruthy();
		expect(creates).toEqual([
			{ orgId: "org-1", name: "example-solution-editors" },
		]);
		expect(grants).toHaveLength(0);
	});
});
