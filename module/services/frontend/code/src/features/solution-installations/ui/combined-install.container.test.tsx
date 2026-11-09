import { fromJsonString, toJson } from "@bufbuild/protobuf";
import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Page from "@/app/admin/solutions/page";
import { ReviewedSolutionInstallSchema } from "@/gen/saas/accounts/v1/installations_pb";
import { setToken } from "@/lib/connect/token-store";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		user: { id: "person-1" },
		isAuthenticated: true,
		orgRole: "owner",
	}),
}));
vi.mock("sonner", () => ({
	toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}));
const target = "aaaaaaaa-0000-4000-8000-000000000001";
const approval = {
	targetId: "signed-solution-principal",
	generation: "2",
	digest: "offer-digest",
	operationId: "bbbbbbbb-0000-4000-8000-000000000001",
};
const review = {
	orgId: "org-1",
	actorId: "person-1",
	targetId: target,
	target: "Example Solution",
	bindingId: "example-binding",
	release: "acme/example@1.0.0",
	deliveryDigest: "delivery-digest",
	installationRole: "none",
	approval,
	modules: [
		{
			principal: "module:records",
			targetId: "records-target",
			target: "Records",
			release: "acme/records@2.0.0",
			required: true,
			installationRole: "none",
			scope:
				'{"capabilities":true,"queues":["index"],"namespaces":["example"],"external_namespaces":["example"],"read_audiences":{"records":{"audience":"records","scopes":[{"resource_kind":"document","resource_ids":["approved-folder"]}]}}}',
			approval: {
				targetId: "records-target",
				generation: "3",
				expectedRevision: "7",
				operationId: "cccccccc-0000-4000-8000-000000000001",
			},
		},
		{
			principal: "module:runtime",
			target: "Runtime",
			required: true,
			reuseInstallationId: "existing-installation",
			scope: '{"capabilities":true}',
		},
		{ principal: "module:optional", required: false },
	],
};
const offer = {
	target: review.target,
	installationRole: "none",
	approval: { ...approval, operationId: "" },
};
let roles: {
	name: string;
	scopes?: { resourceKind: string; actions: string[] }[];
	reach?: {
		path: string[];
		module: string;
		via: string;
		confinement?: string;
		scopes?: { resourceKind: string; actions: string[] }[];
		operations?: string[];
	}[];
}[];
let modules: unknown[] | undefined;
let teams: { id: string; name: string }[];
let requests: unknown[];
let reviews: number;
let mismatch: boolean;
let refuseReview: boolean;
let installed: boolean;
let accessProblems: string[];
let readOnlyRequests: string[];
beforeEach(() => {
	vi.clearAllMocks();
	window.sessionStorage.clear();
	roles = [];
	modules = undefined;
	teams = [];
	requests = [];
	reviews = 0;
	mismatch = false;
	refuseReview = false;
	installed = false;
	accessProblems = [];
	readOnlyRequests = [];
	setToken("example-session-token");
	server.use(
		http.post(rpc("TeamService", "ListTeams"), () =>
			HttpResponse.json({ teams }),
		),
		http.post(rpc("InstallationService", "ListAvailableSolutions"), () =>
			HttpResponse.json({
				solutions: [
					{
						targetId: target,
						routeAlias: review.target,
						kind: "solution",
						releasePublisher: "acme",
						releaseName: "example",
						releaseVersion: "1.0.0",
						appliedGeneration: "2",
						installed,
					},
					{
						targetId: "support",
						routeAlias: "Supporting module",
						kind: "module",
					},
				],
			}),
		),
		http.post(rpc("InstallationService", "ListOrganizationInstallations"), () =>
			HttpResponse.json({ installations: [] }),
		),
		http.post(
			rpc("InstallationService", "ReviewSolutionInstall"),
			async ({ request }) => {
				reviews++;
				readOnlyRequests.push(request.headers.get("Authorization") ?? "");
				if (refuseReview)
					return HttpResponse.json(
						{
							code: "failed_precondition",
							message:
								"Required module records has no installation authority declaration",
						},
						{ status: 400 },
					);
				return HttpResponse.json(modules ? { ...review, modules } : review);
			},
		),
		http.post(
			"http://localhost:3000/host.installation.v1.OfferService/ReviewOffer",
			({ request }) => {
				readOnlyRequests.push(request.headers.get("Authorization") ?? "");
				return HttpResponse.json(
					mismatch
						? { ...offer, roles, approval: { ...approval, digest: "changed" } }
						: { ...offer, roles },
				);
			},
		),
		http.post(
			rpc("InstallationService", "InstallReviewedSolution"),
			async ({ request }) => {
				requests.push(await request.json());
				readOnlyRequests.push(request.headers.get("Authorization") ?? "");
				if (requests.length === 1)
					return HttpResponse.json(
						{ code: "unavailable", message: "Setup has not acknowledged yet" },
						{ status: 503 },
					);
				installed = true;
				return HttpResponse.json({
					id: "installed",
					orgId: "org-1",
					targetId: target,
					status: "INSTALLATION_STATUS_ACTIVE",
					accessProblems,
				});
			},
		),
	);
});
afterEach(() => {
	cleanup();
	setToken(null);
});
async function open() {
	fireEvent.click(
		await screen.findByRole("button", { name: `Install ${review.target}` }),
	);
	return screen.findByRole("dialog");
}

describe("combined reviewed installation", () => {
	it("defaults to solutions, shows exact access and submits the retained review on retry with the current session", async () => {
		renderInApp(<Page />);
		expect(
			screen.queryByRole("button", { name: "Install Supporting module" }),
		).toBeNull();
		let dialog = await open();
		await within(dialog).findByText(/Also installs: Records/);
		expect(
			within(dialog).getByText("Already installed and reused: Runtime"),
		).toBeTruthy();
		expect(
			within(dialog).getByText("Optional, not installed: module:optional"),
		).toBeTruthy();
		expect(within(dialog).queryByText(/Run jobs in: index/)).toBeNull();
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Show technical access" }),
		);
		expect(within(dialog).getByText(/Run jobs in: index/)).toBeTruthy();
		expect(
			within(dialog).getByText("Publish events in namespaces: example."),
		).toBeTruthy();
		expect(
			within(dialog).getByText(
				"Namespaces whose declared external events may be delivered to this organization's configured outbound webhook: example.",
			),
		).toBeTruthy();
		expect(within(dialog).queryByText(/storage spaces/)).toBeNull();
		expect(within(dialog).getByText(/approved-folder/)).toBeTruthy();
		expect(within(dialog).queryByLabelText("Agent identifier")).toBeNull();
		expect(within(dialog).queryByLabelText("Agent role")).toBeNull();
		expect(within(dialog).queryByLabelText("Allowed audiences")).toBeNull();
		const submit = within(dialog).getByRole("button", {
			name: "Install",
		});
		expect((submit as HTMLButtonElement).disabled).toBe(true);
		fireEvent.click(within(dialog).getByRole("checkbox"));
		fireEvent.click(submit);
		await within(dialog).findByRole("alert");
		fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
		dialog = await open();
		await within(dialog).findByText(/Also installs: Records/);
		fireEvent.click(within(dialog).getByRole("checkbox"));
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
		await waitFor(() => expect(requests).toHaveLength(2));
		expect(requests[0]).toEqual({
			orgId: "org-1",
			review: toJson(
				ReviewedSolutionInstallSchema,
				fromJsonString(ReviewedSolutionInstallSchema, JSON.stringify(review)),
			),
		});
		expect(requests[1]).toEqual(requests[0]);
		expect(reviews).toBe(1);
		expect(
			readOnlyRequests.every(
				(header) => header === "Bearer example-session-token",
			),
		).toBe(true);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
	it("refuses to submit a changed shared offer and requires an explicit new review", async () => {
		mismatch = true;
		renderInApp(<Page />);
		const dialog = await open();
		await within(dialog).findByText(/solution changed while preparing/);
		expect(
			(
				within(dialog).getByRole("button", {
					name: "Install",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		mismatch = false;
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Review again" }),
		);
		await within(dialog).findByText(/Also installs: Records/);
		expect(reviews).toBe(2);
		expect(requests).toEqual([]);
	});
	it("shows a missing owner declaration and sends no installation", async () => {
		refuseReview = true;
		renderInApp(<Page />);
		const dialog = await open();
		await within(dialog).findByText(/Required module records/);
		expect(requests).toEqual([]);
	});
	it('the install dialog shows "You will get" with every role ticked and untickable', async () => {
		roles = [{ name: "installer" }, { name: "viewer" }];
		teams = [{ id: "team-viewers", name: "Example-Solution-Viewer" }];
		renderInApp(<Page />);
		const dialog = await open();
		const access = await within(dialog).findByRole("region", {
			name: "Team access",
		});
		await within(access).findByText(/existing team Example-Solution-Viewer/);
		expect(
			within(access).getByText(
				"You will get the ticked roles; others get access by joining a team, here or in Set up.",
			),
		).toBeTruthy();
		const gets = within(access).getByRole("group", { name: "You will get" });
		const installer = within(gets).getByRole("checkbox", { name: "installer" });
		const viewer = within(gets).getByRole("checkbox", { name: "viewer" });
		expect((installer as HTMLInputElement).checked).toBe(true);
		expect((viewer as HTMLInputElement).checked).toBe(true);
		expect(
			within(access).getByText(
				/example-solution-installer \(will be created\)/,
			),
		).toBeTruthy();
		fireEvent.click(viewer);
		expect((viewer as HTMLInputElement).checked).toBe(false);
		expect((installer as HTMLInputElement).checked).toBe(true);
		// Nothing is picked: approving is all Install waits on.
		fireEvent.click(
			within(dialog).getByRole("checkbox", { name: /I approve this target/ }),
		);
		expect(
			(
				within(dialog).getByRole("button", {
					name: "Install",
				}) as HTMLButtonElement
			).disabled,
		).toBe(false);
	});
	it("Install is one click", async () => {
		roles = [{ name: "installer" }, { name: "viewer" }];
		teams = [{ id: "team-viewers", name: "Example-Solution-Viewer" }];
		renderInApp(<Page />);
		const dialog = await open();
		const access = await within(dialog).findByRole("region", {
			name: "Team access",
		});
		await within(access).findByText(/existing team Example-Solution-Viewer/);
		fireEvent.click(
			within(access).getByRole("checkbox", { name: "installer" }),
		);
		fireEvent.click(
			within(dialog).getByRole("checkbox", { name: /I approve this target/ }),
		);
		fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
		await waitFor(() => expect(requests).toHaveLength(1));
		expect(requests[0]).toMatchObject({
			orgId: "org-1",
			roleTeams: { viewer: "team-viewers" },
			installerRoles: ["viewer"],
		});
	});
	it("the install review leads with what each role can do, grouped by where", async () => {
		const page = { resourceKind: "example.page", actions: ["read"] };
		const writePage = {
			resourceKind: "example.page",
			actions: ["read", "write"],
		};
		const document = { resourceKind: "documents", actions: ["read"] };
		const row = (
			via: string,
			module: string,
			operations: string[],
			confinement = "",
			scopes = [page],
		) => ({
			path: via === "role" ? [review.target] : [review.target, module],
			module,
			via,
			confinement,
			scopes: via === "person" ? [] : scopes,
			operations,
		});
		roles = [
			{
				name: "editor",
				scopes: [writePage],
				reach: [
					row(
						"role",
						review.target,
						["GET /pages/{id}", "DELETE /pages/{id}"],
						"installation",
						[writePage],
					),
					row(
						"binding",
						"documents",
						["/docs.Documents/ReadDocument", "/docs.Documents/ListDocuments"],
						"",
						[document],
					),
					row("person", "directory", []),
				],
			},
			{
				name: "viewer",
				scopes: [page],
				reach: [
					row("role", review.target, ["GET /pages/{id}"], "installation"),
				],
			},
		];
		renderInApp(<Page />);
		const dialog = await open();
		const reach = await within(dialog).findByRole("region", {
			name: "What people can do",
		});
		const text = reach.textContent ?? "";
		const groups = [
			"In Example Solution:",
			"Through Example Solution, in documents:",
			"Uses your own access in directory:",
		];
		for (const group of groups)
			expect(within(reach).getByText(group)).toBeTruthy();
		// Direct first, then through the solution, then the holder's own access.
		expect(groups.map((g) => text.indexOf(g))).toEqual(
			[...groups.map((g) => text.indexOf(g))].sort((a, b) => a - b),
		);
		// What each role grants there, never the operations a target with no
		// operation policy lets every role reach.
		expect(text).toContain(
			"editor: read and write pages (this installation's items)",
		);
		expect(text).toContain("viewer: read pages (this installation's items)");
		expect(text).not.toMatch(/delete pages/);
		expect(text).toContain("editor: read documents");
		expect(text).toContain("editor: what you hold there yourself");
		// Operations in plain words; identifiers only behind the disclosure.
		expect(dialog.textContent).not.toMatch(/docs\.Documents|\{id\}/);
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Show technical access" }),
		);
		const technical = within(dialog).getByRole("region", {
			name: "Technical access",
		});
		expect(technical.textContent).toContain(
			"editor — binding Example Solution → documents: /docs.Documents/ReadDocument, /docs.Documents/ListDocuments",
		);
		const supporting = within(dialog).getByRole("region", {
			name: "Supporting modules",
		});
		expect(
			reach.compareDocumentPosition(supporting) &
				Node.DOCUMENT_POSITION_FOLLOWING,
		).toBeTruthy();
	});
	it("supporting modules are one line; technical identifiers only behind a disclosure", async () => {
		// Each module installed here declares one operation on two faces.
		const twoFaces = JSON.stringify({
			capabilities: true,
			operation_audiences: {
				EXAMPLE__ITEMS_CONNECT_LIST: {
					audience: "example-items",
					delegation_mode: "module_actor",
					invoke_scopes: [{ resource_kind: "item", actions: ["list"] }],
				},
				EXAMPLE__ITEMS_GRPC_LIST: {
					audience: "example-items",
					delegation_mode: "module_actor",
					invoke_scopes: [{ resource_kind: "item", actions: ["list"] }],
				},
			},
		});
		const module = (name: string, reused: boolean) => ({
			principal: `module:${name}`,
			target: name,
			required: true,
			installationRole: "none",
			scope: reused
				? '{"capabilities":true,"queues":["reused-queue"]}'
				: twoFaces,
			...(reused ? { reuseInstallationId: `installed-${name}` } : {}),
			approval: {
				targetId: `${name}-target`,
				generation: "1",
				operationId: approval.operationId,
			},
		});
		modules = [
			...["Alpha", "Bravo", "Charlie", "Delta"].map((name) =>
				module(name, false),
			),
			...["Echo", "Foxtrot", "Golf", "Hotel"].map((name) => module(name, true)),
		];
		renderInApp(<Page />);
		const dialog = await open();
		const supporting = await within(dialog).findByRole("region", {
			name: "Supporting modules",
		});
		expect(
			within(supporting).getByText(
				"Also installs: Alpha, Bravo, Charlie, Delta — no standing machine access",
			),
		).toBeTruthy();
		expect(
			within(supporting).getByText(
				"Already installed and reused: Echo, Foxtrot, Golf, Hotel",
			),
		).toBeTruthy();
		expect(within(dialog).queryByText(/Perform operations/)).toBeNull();
		expect(dialog.textContent).not.toMatch(/EXAMPLE__|Run jobs in/);
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Show technical access" }),
		);
		const technical = within(dialog).getByRole("region", {
			name: "Technical access",
		});
		// One line per module for the operation both faces reach, never its id.
		expect(
			within(technical).getAllByText(
				"Perform operations through example-items: list item; all items of this type.",
			),
		).toHaveLength(4);
		expect(
			within(technical).getAllByText(
				"Acts at example-items: delegation mode module actor.",
			),
		).toHaveLength(4);
		expect(technical.textContent).not.toMatch(/EXAMPLE__/);
		// A reused module's consent is unchanged: no detail for it.
		expect(technical.textContent).not.toMatch(
			/Echo|Foxtrot|Golf|Hotel|reused-queue/,
		);
	});
	it("an install whose access is not fully applied says what is left to grant", async () => {
		roles = [{ name: "installer" }, { name: "viewer" }];
		accessProblems = [
			"viewer: the role could not be granted to its team: refused",
		];
		renderInApp(<Page />);
		const install = async () => {
			const dialog = await open();
			await within(dialog).findByRole("region", { name: "Team access" });
			fireEvent.click(
				within(dialog).getByRole("checkbox", { name: /I approve this target/ }),
			);
			fireEvent.click(within(dialog).getByRole("button", { name: "Install" }));
			return dialog;
		};
		const first = await install();
		await within(first).findByRole("alert");
		fireEvent.click(within(first).getByRole("button", { name: "Cancel" }));
		await install();
		await waitFor(() => expect(toast.warning).toHaveBeenCalledTimes(1));
		expect(vi.mocked(toast.warning).mock.calls[0][0]).toContain(
			"viewer: the role could not be granted to its team: refused",
		);
		expect(toast.success).not.toHaveBeenCalledWith(
			"Installed. Each role's team holds it now.",
		);
	});
});
