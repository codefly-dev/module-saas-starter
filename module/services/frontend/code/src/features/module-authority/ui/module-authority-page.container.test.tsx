import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http, type JsonBodyType } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ModuleAuthorityPage } from "./module-authority-page";

afterEach(cleanup);

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

async function write(revision: string) {
	fireEvent.click(
		await screen.findByRole("button", { name: `Write revision ${revision}` }),
	);
	const dialog = await screen.findByRole("alertdialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Write" }));
}

describe("ModuleAuthorityPage", () => {
	it("says no envelope exists and names every principal it therefore denies", async () => {
		serveEnvelope({
			requested: [asking("documents", false), asking("reports", false)],
		});
		renderInApp(<ModuleAuthorityPage />);

		expect(
			await screen.findByText("No module envelope has been written"),
		).toBeTruthy();
		expect(
			screen.getByText(
				/^2 of 2 asked for have no ceiling in the envelope, and are denied every Work Context\./,
			),
		).toBeTruthy();
		const table = screen.getByRole("table", {
			name: "Authority the applied manifests ask for",
		});
		expect(within(table).getByText("documents")).toBeTruthy();
		expect(within(table).getAllByText("No ceiling")).toHaveLength(2);
		const draft = screen.getByLabelText(
			"Envelope document",
		) as HTMLTextAreaElement;
		expect(JSON.parse(draft.value)).toEqual({ revision: 1, principals: {} });
	});

	it("writes the next revision fenced on the revision the draft started from", async () => {
		serveEnvelope({
			envelope: revision3,
			requested: [asking("documents", false), asking("reports", true)],
		});
		const sent = serveWrite(() =>
			HttpResponse.json({ envelope: { ...revision3, revision: "4" } }),
		);
		renderInApp(<ModuleAuthorityPage />);

		expect(await screen.findByText(revision3.writtenBy)).toBeTruthy();
		expect(
			screen.getByText(
				/^1 of 2 asked for has no ceiling in the envelope, and is denied every Work Context\./,
			),
		).toBeTruthy();
		const draft = screen.getByLabelText(
			"Envelope document",
		) as HTMLTextAreaElement;
		expect(JSON.parse(draft.value).revision).toBe(4);
		fireEvent.change(draft, {
			target: {
				value: JSON.stringify({ revision: 4, principals: { documents: {} } }),
			},
		});
		await write("4");

		await waitFor(() => expect(sent).toHaveLength(1));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(screen.queryByText("Not written")).toBeNull();
		expect(sent[0].expectedRevision).toBe("3");
		expect(JSON.parse(sent[0].document ?? "")).toEqual({
			revision: 4,
			principals: { documents: {} },
		});
	});

	it("keeps the draft fenced on its own revision when another one is written meanwhile", async () => {
		serveEnvelope({ envelope: revision3, requested: [] });
		const sent = serveWrite(() =>
			HttpResponse.json(
				{ code: "aborted", message: "written since" },
				{ status: 409 },
			),
		);
		const { client } = renderInApp(<ModuleAuthorityPage />);
		const draft = (await screen.findByLabelText(
			"Envelope document",
		)) as HTMLTextAreaElement;
		fireEvent.change(draft, {
			target: { value: JSON.stringify({ revision: 4, principals: {} }) },
		});

		serveEnvelope({ envelope: { ...revision3, revision: "4" }, requested: [] });
		await client.invalidateQueries();
		expect(
			await screen.findByText(
				/Revision 4 was written after this draft was started from revision 3/,
			),
		).toBeTruthy();
		await write("4");

		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0].expectedRevision).toBe("3");
		expect(
			await screen.findByText(/Another administrator wrote a revision/),
		).toBeTruthy();
	});

	it("flags a principal two bindings ask for, and shows an unreadable request as sent", async () => {
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

		expect(await screen.findAllByText("Asked for by 2 bindings")).toHaveLength(
			2,
		);
		expect(screen.getByText("{not json")).toBeTruthy();
	});

	it.each([
		[
			"a session with no recent second factor",
			{ code: "failed_precondition", message: "mfa_required" },
			400,
			"Writing the envelope needs your passkey confirmed within the last 15 minutes. Confirm it (the sign-in page brings you back here), then write it again.",
		],
		[
			"a revision another administrator wrote first",
			{ code: "aborted", message: "the module envelope was written since" },
			409,
			"Another administrator wrote a revision after you started this draft. Nothing was written: discard the draft, review their revision, and decide again.",
		],
		[
			"a document the host refuses",
			{
				code: "invalid_argument",
				message: "module envelope is invalid: unknown field workload",
			},
			400,
			"module envelope is invalid: unknown field workload",
		],
	])(
		"names the reason a write was refused: %s",
		async (_name, body, status, reason) => {
			serveEnvelope({ requested: [asking("documents", false)] });
			serveWrite(() => HttpResponse.json(body, { status }));
			renderInApp(<ModuleAuthorityPage />);

			await write("1");

			expect(await screen.findByText(reason)).toBeTruthy();
			expect(screen.getByText("Not written")).toBeTruthy();
		},
	);

	it("shows why the envelope could not be read", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetModuleEnvelope"), () =>
				HttpResponse.json(
					{
						code: "failed_precondition",
						message: "this host has no module envelope store",
					},
					{ status: 400 },
				),
			),
		);
		renderInApp(<ModuleAuthorityPage />);

		expect((await screen.findByRole("alert")).textContent).toContain(
			"this host has no module envelope store",
		);
	});
});
