import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { PermissionsPage } from "./permissions-page";

const session = vi.hoisted(() => ({
	organizationId: "org-1" as string | undefined,
	orgRole: "owner" as OrgRole | undefined,
	platformRole: undefined as PlatformRole | undefined,
	switchOrganization: vi.fn(async () => undefined),
}));

vi.mock("@/lib/auth", () => ({ useAuth: () => session }));

function serveVocabulary() {
	server.use(
		http.post(rpc("IntrospectionService", "GetServiceInfo"), () =>
			HttpResponse.json({
				capabilities: {
					permissions: [
						{ resource: "users", action: "read", description: "List users." },
						{
							resource: "audit",
							action: "read",
							description: "Read audit events.",
						},
					],
				},
			}),
		),
		http.post(rpc("PermissionService", "ListRoles"), () =>
			HttpResponse.json({
				roles: [
					{
						id: "role-admin",
						name: "admin",
						permissions: [{ resource: "*", action: "*" }],
						builtIn: true,
						orgId: "",
						description: "",
					},
					{
						id: "role-auditor",
						name: "auditor",
						permissions: [{ resource: "audit", action: "read" }],
						builtIn: false,
						orgId: "org-1",
						description: "",
					},
				],
			}),
		),
		http.post(rpc("OrganizationService", "ListMembers"), () =>
			HttpResponse.json({ members: [] }),
		),
		http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
			HttpResponse.json({ assignments: [] }),
		),
	);
}

beforeEach(() => {
	session.organizationId = "org-1";
	session.orgRole = "owner";
	session.platformRole = undefined;
});

afterEach(cleanup);

describe("PermissionsPage", () => {
	it("groups the declared vocabulary by the resource it acts on", async () => {
		serveVocabulary();

		renderInApp(<PermissionsPage />);

		expect(await screen.findByRole("heading", { name: "audit" })).toBeTruthy();
		expect(screen.getByRole("heading", { name: "users" })).toBeTruthy();
		expect(screen.getByText("Read audit events.")).toBeTruthy();
	});

	// "Who can do X" has to count the wildcard role, or an administrator reads
	// "only auditor" off a page and revokes the wrong grant.
	it("counts a wildcard role among the roles granting a named permission", async () => {
		serveVocabulary();

		renderInApp(<PermissionsPage />);

		const auditRow = (await screen.findByText("audit:read")).closest("tr");
		expect(auditRow?.textContent).toContain("admin");
		expect(auditRow?.textContent).toContain("auditor");

		const usersRow = screen.getByText("users:read").closest("tr");
		expect(usersRow?.textContent).toContain("admin");
		expect(usersRow?.textContent).not.toContain("auditor");
	});
});

// The control an administrator uses to verify a grant before relying on it.
// Its answer comes from the authorization service (ExplainPermission), so
// these tests pin what the page does with that answer rather than what a
// client-side reading of the role rows would have said.
describe("PermissionsPage — check a grant", () => {
	const MEMBER = "11111111-1111-4111-8111-111111111111";

	function serveCheckable(
		explain: (body: Record<string, unknown>) => Response,
		assignments: unknown[] = [],
	) {
		serveVocabulary();
		server.use(
			http.post(rpc("OrganizationService", "ListMembers"), () =>
				HttpResponse.json({ members: [{ userId: MEMBER }] }),
			),
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json({ teams: [] }),
			),
			http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
				HttpResponse.json({ assignments }),
			),
			http.post(
				rpc("PermissionService", "ExplainPermission"),
				async ({ request }) =>
					explain((await request.json()) as Record<string, unknown>),
			),
		);
	}

	async function pick(placeholder: string, option: string) {
		const trigger = (await screen.findByText(placeholder)).closest("button");
		fireEvent.click(trigger as HTMLElement);
		const item = await screen.findByRole("option", { name: option });
		fireEvent.pointerDown(item, { pointerType: "mouse" });
		fireEvent.click(item);
	}

	async function ask(scope?: string) {
		await pick("Pick a member…", `${MEMBER.slice(0, 8)}...`);
		await pick("Pick a permission…", "audit:read");
		if (scope !== undefined) {
			const box = screen.getByLabelText("Scope");
			fireEvent.change(box, { target: { value: scope } });
			// The question is asked on submit, not on every keystroke.
			fireEvent.keyDown(box, { key: "Enter" });
		}
	}

	it("puts the question to the authorization service, not to the role rows", async () => {
		const asked: Record<string, unknown>[] = [];
		serveCheckable((body) => {
			asked.push(body);
			return HttpResponse.json({
				allowed: true,
				reason: "granted via role: auditor",
				grantingScopes: [],
			});
		});

		renderInApp(<PermissionsPage />);
		await ask();

		expect(await screen.findByText("granted via role: auditor")).toBeTruthy();
		expect(screen.getByText("Allowed organization-wide")).toBeTruthy();
		// SubjectKind.PRINCIPAL — the contract rejects the unspecified value
		// rather than guessing which of the two kinds holds the id. It goes on
		// the wire as SUBJECT_KIND_USER: the two share value 1, and the JSON
		// name is the deprecated alias, which is what the server parses.
		expect(asked).toEqual([
			{
				orgId: "org-1",
				subjectId: MEMBER,
				subjectKind: "SUBJECT_KIND_USER",
				resource: "audit",
				action: "read",
			},
		]);
	});

	// The case the contract warns about: asked at a scope the subject does not
	// hold, the decision is a plain no, and presenting it alone tells an
	// administrator a scoped entitlement does not exist.
	it("shows where a denied subject is entitled instead of a bare no", async () => {
		serveCheckable(() =>
			HttpResponse.json({
				allowed: false,
				reason: "no matching permission found",
				grantingScopes: ["project-42"],
			}),
		);

		renderInApp(<PermissionsPage />);
		await ask("project-7");

		expect(await screen.findByText("Not allowed in project-7")).toBeTruthy();
		expect(screen.getByText("Granted, but only in")).toBeTruthy();
		expect(screen.getByText("project-42")).toBeTruthy();
	});

	// A refusal to answer is not an answer: the service denies a subject it
	// will not speak about, and "not allowed" would be a claim it never made.
	it("reports a refusal as a refusal, not as a denial", async () => {
		serveCheckable(() => new HttpResponse(null, { status: 403 }));

		renderInApp(<PermissionsPage />);
		await ask();

		expect(
			await screen.findByText(/authorization service did not answer/),
		).toBeTruthy();
		expect(screen.queryByText(/^Not allowed/)).toBeNull();
	});

	// The service counts a role assigned globally; ListRoleAssignments, which
	// filters to this organization's rows, does not return it. An allowed
	// verdict with no local path is that grant, and saying nothing would leave
	// an administrator hunting for an assignment that is not theirs to revoke.
	it("names the gap when the verdict has no assignment in this organization", async () => {
		serveCheckable(() =>
			HttpResponse.json({
				allowed: true,
				reason: "granted via role: platform-admin",
				grantingScopes: [],
			}),
		);

		renderInApp(<PermissionsPage />);
		await ask();

		expect(
			await screen.findByText(/a role assigned\s+globally grants it/),
		).toBeTruthy();
	});
});

// The scope half of the control: what is typed, what is asked, and what the
// answer is allowed to claim. Each of these pins a way the page could tell an
// administrator something untrue about access.
describe("PermissionsPage — the scope a question carries", () => {
	const MEMBER = "11111111-1111-4111-8111-111111111111";

	function serve(
		onExplain: (body: Record<string, string>) => void,
		assignments: unknown[] = [],
	) {
		serveVocabulary();
		server.use(
			http.post(rpc("OrganizationService", "ListMembers"), () =>
				HttpResponse.json({ members: [{ userId: MEMBER }] }),
			),
			http.post(rpc("TeamService", "ListTeams"), () =>
				HttpResponse.json({ teams: [] }),
			),
			http.post(rpc("PermissionService", "ListRoleAssignments"), () =>
				HttpResponse.json({ assignments }),
			),
			http.post(
				rpc("PermissionService", "ExplainPermission"),
				async ({ request }) => {
					onExplain((await request.json()) as Record<string, string>);
					return HttpResponse.json({
						allowed: false,
						reason: "no matching permission found",
						grantingScopes: ["project-42"],
					});
				},
			),
		);
	}

	async function choose() {
		const open = async (placeholder: string, option: string) => {
			fireEvent.click(
				(await screen.findByText(placeholder)).closest("button") as HTMLElement,
			);
			const item = await screen.findByRole("option", { name: option });
			fireEvent.pointerDown(item, { pointerType: "mouse" });
			fireEvent.click(item);
		};
		await open("Pick a member…", `${MEMBER.slice(0, 8)}...`);
		await open("Pick a permission…", "audit:read");
	}

	// Without this, the scope box could be wired to nothing at all: the badge
	// text is rendered from local state, so every other assertion still passes
	// while the service is asked the organization-wide question.
	it("sends the scope on the wire, not just into the badge", async () => {
		const asked: Record<string, string>[] = [];
		serve((body) => asked.push(body));

		renderInApp(<PermissionsPage />);
		await choose();
		const box = screen.getByLabelText("Scope");
		fireEvent.change(box, { target: { value: "project-42" } });
		fireEvent.keyDown(box, { key: "Enter" });

		await screen.findByText("Not allowed in project-42");
		expect(asked.map((body) => body.scope ?? "")).toContain("project-42");
	});

	// Typing is not asking. Every intermediate string would otherwise be a
	// question, and each answer a confident denial for a scope that does not
	// exist.
	it("asks once on submit rather than once per keystroke", async () => {
		const asked: Record<string, string>[] = [];
		serve((body) => asked.push(body));

		renderInApp(<PermissionsPage />);
		await choose();
		await screen.findByText("Not allowed organization-wide");
		const before = asked.length;

		const box = screen.getByLabelText("Scope");
		for (const value of ["p", "pr", "pro", "proj", "project-42"]) {
			fireEvent.change(box, { target: { value } });
		}
		expect(screen.queryByText(/^Not allowed in/)).toBeNull();
		expect(asked.length).toBe(before);

		fireEvent.keyDown(box, { key: "Enter" });
		await screen.findByText("Not allowed in project-42");
		expect(asked.length).toBe(before + 1);
		expect(asked.map((body) => body.scope ?? "")).not.toContain("pro");
	});

	// A scope the service can never match, because of whitespace the
	// administrator cannot see, would read as a plain denial.
	it("asks about the trimmed scope", async () => {
		const asked: Record<string, string>[] = [];
		serve((body) => asked.push(body));

		renderInApp(<PermissionsPage />);
		await choose();
		const box = screen.getByLabelText("Scope");
		fireEvent.change(box, { target: { value: "  project-42\n" } });
		fireEvent.keyDown(box, { key: "Enter" });

		await screen.findByText("Not allowed in project-42");
		expect(asked.map((body) => body.scope ?? "")).toContain("project-42");
	});

	// The regression this whole pass exists for: a role scoped elsewhere is not
	// a path that explains an organization-wide denial.
	it("does not offer a scoped grant as the path behind an org-wide denial", async () => {
		serve(() => {}, [
			{ subjectId: MEMBER, roleId: "role-auditor", scope: "project-42" },
		]);

		renderInApp(<PermissionsPage />);
		await choose();

		expect(
			await screen.findByText("Not allowed organization-wide"),
		).toBeTruthy();
		// Where the subject IS entitled is the honest answer, and it comes from
		// the service's own granting_scopes.
		expect(screen.getByText("Granted, but only in")).toBeTruthy();
		// No path list of any wording: asserting on one label would pass the
		// moment the denial's wording changed, while the scoped grant was still
		// being offered as an explanation.
		expect(screen.queryByText("auditor in project-42")).toBeNull();
		expect(screen.queryByText("Through")).toBeNull();
		expect(screen.queryByText(/despite these assignments/)).toBeNull();
	});
});
