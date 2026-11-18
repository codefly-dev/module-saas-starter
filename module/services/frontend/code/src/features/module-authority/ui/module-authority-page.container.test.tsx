import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { delay, HttpResponse, http, type JsonBodyType } from "msw";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ModuleAuthorityPage } from "./module-authority-page";

// The operator reading the page. "Changed by" names a person, and the person
// who made the last change here is the one signed in.
const OPERATOR = "019f6bf7-5b1c-730d-9687-fe6d4aff31ee";
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		user: { id: OPERATOR, name: "Jane Doe", email: "jane@example.com" },
		platformRole: "super_admin",
		organizationId: "org-1",
	}),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
	cleanup();
	vi.clearAllMocks();
});

const asking = (principal: string, allowed: boolean) => ({
	principal,
	binding: `acme.test.${principal}`,
	generation: "2",
	alias: principal,
	allowed,
	authority: JSON.stringify({ principal, queues: [`${principal}.render`] }),
});

const revision3 = {
	revision: "3",
	document: JSON.stringify({
		revision: 3,
		principals: { reports: { grant: { queues: ["reports.render"] } } },
	}),
	writtenBy: "019f6bf7-5b1c-730d-9687-fe6d4aff31ee",
	writtenAt: "2026-10-18T10:00:00Z",
};

function serveEnvelope(body: JsonBodyType) {
	server.use(
		http.post(rpc("PlatformAdminService", "GetModuleEnvelope"), () =>
			HttpResponse.json(body),
		),
	);
}

/** Serves `answer` to every write and records each request it was sent. */
function serveWrite(answer: () => Response) {
	const sent: { expectedRevision?: string; document?: string }[] = [];
	server.use(
		http.post(
			rpc("PlatformAdminService", "PutModuleEnvelope"),
			async ({ request }) => {
				sent.push((await request.json()) as (typeof sent)[number]);
				return answer();
			},
		),
	);
	return sent;
}

/** Presses "Save changes" and answers the question it asks. */
async function save() {
	fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
	const dialog = await screen.findByRole("alertdialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));
	return dialog;
}

const editor = async () =>
	(await screen.findByLabelText("Approved permissions")) as HTMLTextAreaElement;

describe("ModuleAuthorityPage", () => {
	it("says nothing is approved and names every module that is therefore blocked", async () => {
		serveEnvelope({
			requested: [asking("documents", false), asking("reports", false)],
		});
		renderInApp(<ModuleAuthorityPage />);

		expect(
			await screen.findByText("No module has been approved yet"),
		).toBeTruthy();
		expect(
			screen.getByText(
				/^2 of 2 are waiting for approval and can't do any work for organizations until approved\./,
			),
		).toBeTruthy();
		// The table holds its loading appearance long enough to read, so the
		// first header is waited for.
		await screen.findByRole("columnheader", { name: "Module" });
		for (const header of ["Module", "What it asks to do", "Status"]) {
			expect(screen.getByRole("columnheader", { name: header })).toBeTruthy();
		}
		expect(screen.getByText("documents")).toBeTruthy();
		expect(screen.getAllByText("Waiting for approval")).toHaveLength(2);
		expect(screen.getAllByText("Run background work on 1 queue")).toHaveLength(
			2,
		);
		// Nothing is approved, so there is no approval to revoke.
		expect(
			screen.queryByRole("button", { name: /Revoke approval/ }),
		).toBeNull();
		expect(JSON.parse((await editor()).value)).toEqual({
			revision: 1,
			principals: {},
		});
	});

	it("keeps an engineer's identifiers under Technical details, off the main surface", async () => {
		serveEnvelope({
			envelope: revision3,
			requested: [asking("reports", true)],
		});
		renderInApp(<ModuleAuthorityPage />);

		await screen.findByText("reports");
		expect(screen.getByText("Jane Doe")).toBeTruthy();
		expect(screen.queryByText("acme.test.reports")).toBeNull();
		expect(
			screen.queryByText(revision3.writtenBy, { exact: false }),
		).toBeNull();
		const [requests] = screen.getAllByRole("button", {
			name: "Technical details",
		});
		fireEvent.click(requests);
		expect(await screen.findByText("acme.test.reports")).toBeTruthy();
	});

	it("saves the next version fenced on the version the edits started from, naming the module it approves", async () => {
		serveEnvelope({
			envelope: revision3,
			requested: [asking("documents", false), asking("reports", true)],
		});
		const sent = serveWrite(() =>
			HttpResponse.json({ envelope: { ...revision3, revision: "4" } }),
		);
		renderInApp(<ModuleAuthorityPage />);

		expect(
			await screen.findByText(
				/^1 of 2 is waiting for approval and can't do any work for organizations until approved\./,
			),
		).toBeTruthy();
		const draft = await editor();
		expect(JSON.parse(draft.value).revision).toBe(4);
		fireEvent.change(draft, {
			target: {
				value: JSON.stringify({
					revision: 4,
					principals: {
						reports: { grant: { queues: ["reports.render"] } },
						documents: {},
					},
				}),
			},
		});
		const dialog = await save();
		expect(
			within(dialog).getByText("Approve the module documents?"),
		).toBeTruthy();
		expect(dialog.textContent).toContain(
			"documents will be able to do what you approved, for every organization on this platform.",
		);

		await waitFor(() => expect(sent).toHaveLength(1));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(screen.queryByText("Not saved")).toBeNull();
		expect(toast.success).toHaveBeenCalledWith("Module permissions saved");
		expect(sent[0].expectedRevision).toBe("3");
		expect(JSON.parse(sent[0].document ?? "").principals.documents).toEqual({});
	});

	it("revokes one module's approval after a question that names it and who is affected", async () => {
		serveEnvelope({
			envelope: revision3,
			requested: [asking("documents", false), asking("reports", true)],
		});
		const sent = serveWrite(() =>
			HttpResponse.json({ envelope: { ...revision3, revision: "4" } }),
		);
		renderInApp(<ModuleAuthorityPage />);

		fireEvent.click(
			await screen.findByRole("button", {
				name: "Revoke approval for reports",
			}),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Revoke approval for the module reports?"),
		).toBeTruthy();
		expect(dialog.textContent).toContain(
			"reports will stop working for every organization on this platform",
		);
		// Asking is not doing.
		expect(sent).toHaveLength(0);
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Revoke approval" }),
		);

		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0].expectedRevision).toBe("3");
		expect(JSON.parse(sent[0].document ?? "")).toEqual({
			revision: 4,
			principals: {},
		});
		await waitFor(() =>
			expect(toast.success).toHaveBeenCalledWith(
				"Approval revoked for reports",
			),
		);
	});

	it("leaves the approval alone when the question is cancelled", async () => {
		serveEnvelope({
			envelope: revision3,
			requested: [asking("reports", true)],
		});
		const sent = serveWrite(() => HttpResponse.json({}));
		renderInApp(<ModuleAuthorityPage />);

		fireEvent.click(
			await screen.findByRole("button", {
				name: "Revoke approval for reports",
			}),
		);
		const dialog = await screen.findByRole("alertdialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(sent).toHaveLength(0);
	});

	it("keeps the edits fenced on their own version when another one is saved meanwhile", async () => {
		serveEnvelope({ envelope: revision3, requested: [] });
		const sent = serveWrite(() =>
			HttpResponse.json(
				{ code: "aborted", message: "written since" },
				{ status: 409 },
			),
		);
		const { client } = renderInApp(<ModuleAuthorityPage />);
		fireEvent.change(await editor(), {
			target: { value: JSON.stringify({ revision: 4, principals: {} }) },
		});

		serveEnvelope({ envelope: { ...revision3, revision: "4" }, requested: [] });
		await client.invalidateQueries();
		expect(
			await screen.findByText(
				/Version 4 was saved after you started editing version 3/,
			),
		).toBeTruthy();
		await save();

		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0].expectedRevision).toBe("3");
		expect(
			await screen.findByText(/Another operator saved a newer version first/),
		).toBeTruthy();
	});

	it("flags a module installed twice under one name", async () => {
		serveEnvelope({
			envelope: revision3,
			requested: [
				asking("reports", true),
				{
					...asking("reports", true),
					binding: "acme.test.other",
					authority: "{not json",
				},
			],
		});
		renderInApp(<ModuleAuthorityPage />);

		expect(
			await screen.findAllByText("Blocked: installed 2 times"),
		).toHaveLength(2);
		// A request the page cannot read is not summarised by guessing.
		expect(
			screen.getByText("The full request is under Technical details."),
		).toBeTruthy();
		fireEvent.click(
			screen.getAllByRole("button", { name: "Technical details" })[0],
		);
		expect(await screen.findByText("{not json")).toBeTruthy();
	});

	it("says beside the field that the document cannot be read, and does not offer to save it", async () => {
		serveEnvelope({ envelope: revision3, requested: [] });
		renderInApp(<ModuleAuthorityPage />);

		fireEvent.change(await editor(), { target: { value: "{not json" } });
		expect(
			screen.getByText(
				"This isn't valid JSON. Check for a missing comma, quote or bracket.",
			),
		).toBeTruthy();
		expect(
			(
				screen.getByRole("button", {
					name: "Save changes",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
	});

	it.each([
		[
			"a session with no recent second factor",
			{ code: "failed_precondition", message: "mfa_required" },
			400,
			"Saving needs your passkey confirmed within the last 15 minutes. Confirm it (the sign-in page brings you back here), then save again.",
		],
		[
			"a version another operator saved first",
			{ code: "aborted", message: "the module envelope was written since" },
			409,
			"Another operator saved a newer version first. Nothing was saved: discard any changes you were making, review their version, and decide again.",
		],
		[
			"a document the platform refuses",
			{
				code: "invalid_argument",
				message: "module envelope is invalid: unknown field workload",
			},
			400,
			"The platform didn't accept these permissions, so nothing was saved. Check the document against the format under Technical details: a module's entry may hold only its permissions.",
		],
	])(
		"says why a save was refused in its own words, never the server's: %s",
		async (_name, body, status, reason) => {
			serveEnvelope({ requested: [asking("documents", false)] });
			serveWrite(() => HttpResponse.json(body, { status }));
			renderInApp(<ModuleAuthorityPage />);

			await save();

			expect(await screen.findByText(reason)).toBeTruthy();
			expect(screen.getByText("Not saved")).toBeTruthy();
			expect(screen.queryByText(body.message, { exact: false })).toBeNull();
		},
	);

	it("says no module is asking, rather than showing an empty table", async () => {
		serveEnvelope({ envelope: revision3, requested: [] });
		renderInApp(<ModuleAuthorityPage />);

		expect(
			await screen.findByText("No module is asking for permissions"),
		).toBeTruthy();
		expect(screen.queryByRole("table")).toBeNull();
	});

	it("shows a loading table, never the empty state, while the read is in flight", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetModuleEnvelope"), async () => {
				await delay(600);
				return HttpResponse.json({ envelope: revision3, requested: [] });
			}),
		);
		renderInApp(<ModuleAuthorityPage />);

		expect(await screen.findByLabelText("Loading table")).toBeTruthy();
		expect(
			screen.queryByText("No module is asking for permissions"),
		).toBeNull();
		expect(screen.queryByText("No module has been approved yet")).toBeNull();
		expect(
			await screen.findByText("No module is asking for permissions"),
		).toBeTruthy();
	});

	it("tells a reader without the role who can grant it, and offers no retry", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetModuleEnvelope"), () =>
				HttpResponse.json(
					{
						code: "permission_denied",
						message: "requires platform super_admin role",
					},
					{ status: 403 },
				),
			),
		);
		renderInApp(<ModuleAuthorityPage />);

		const refusal = await screen.findByRole("alert");
		expect(refusal.textContent).toContain(
			"You don't have access to module permissions",
		);
		expect(refusal.textContent).toContain("Ask one to give you that role.");
		expect(refusal.textContent).not.toContain("super_admin");
		expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
	});

	it("says a failed read failed in plain words, and retries it", async () => {
		let healthy = false;
		server.use(
			http.post(rpc("PlatformAdminService", "GetModuleEnvelope"), () =>
				healthy
					? HttpResponse.json({ envelope: revision3, requested: [] })
					: HttpResponse.json(
							{
								code: "failed_precondition",
								message: "this host has no module envelope store",
							},
							{ status: 400 },
						),
			),
		);
		renderInApp(<ModuleAuthorityPage />);

		const failure = await screen.findByRole("alert");
		expect(failure.textContent).toContain(
			"Module permissions couldn't be loaded",
		);
		expect(failure.textContent).not.toContain("envelope store");

		healthy = true;
		fireEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(
			await screen.findByText("No module is asking for permissions"),
		).toBeTruthy();
	});
});
