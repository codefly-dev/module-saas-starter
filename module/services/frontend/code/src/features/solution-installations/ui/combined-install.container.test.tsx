import { fromJsonString, toJson } from "@bufbuild/protobuf";
import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Page from "@/app/admin/solutions/page";
import { ReviewedSolutionInstallSchema } from "@/gen/saas/accounts/v1/installations_pb";
import { setToken } from "@/lib/connect/token-store";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		user: { id: "person-1" },
		isAuthenticated: true,
		orgRole: "owner",
	}),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
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
				'{"capabilities":true,"queues":["index"],"read_audiences":{"records":{"audience":"records","scopes":[{"resource_kind":"document","resource_ids":["approved-folder"]}]}}}',
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
let requests: unknown[];
let reviews: number;
let mismatch: boolean;
let refuseReview: boolean;
let installed: boolean;
let readOnlyRequests: string[];
beforeEach(() => {
	requests = [];
	reviews = 0;
	mismatch = false;
	refuseReview = false;
	installed = false;
	readOnlyRequests = [];
	setToken("example-session-token");
	server.use(
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
				return HttpResponse.json(review);
			},
		),
		http.post(
			"http://localhost:3000/host.installation.v1.OfferService/ReviewOffer",
			({ request }) => {
				readOnlyRequests.push(request.headers.get("Authorization") ?? "");
				return HttpResponse.json(
					mismatch
						? { ...offer, approval: { ...approval, digest: "changed" } }
						: offer,
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
		await within(dialog).findByText("Records");
		expect(
			within(dialog).getByText(/Already installed — existing access/),
		).toBeTruthy();
		expect(
			within(dialog).getByText(/Optional — will not be installed/),
		).toBeTruthy();
		expect(within(dialog).getByText(/Run jobs in: index/)).toBeTruthy();
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
		await within(dialog).findByText("Records");
		fireEvent.click(within(dialog).getByRole("checkbox"));
		fireEvent.click(
			within(dialog).getByRole("button", { name: "Install" }),
		);
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
		await within(dialog).findByText("Records");
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
});
