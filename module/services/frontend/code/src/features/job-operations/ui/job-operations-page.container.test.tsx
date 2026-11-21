import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { delay, HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { JobOperationsPage } from "./job-operations-page";

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

// Every read routes through PlatformAdminService (saas.accounts.v1), even
// though the message types live in saas.jobs.v1.
const admin = (method: string) => rpc("PlatformAdminService", method);

const EMAILS = {
	queue: "emails",
	ready: "5",
	processing: "2",
	scheduled: "1",
	deadLetter: "3",
	expiredLeases: "0",
};

// A job that failed for good, carrying the code and the engineer's message the
// page must never show.
const RAW_CODE = "datasource.github_rate_limited";
const RAW_MESSAGE =
	"GET https://api.github.com/repos: 403 secondary rate limit";
const FAILED_JOB = {
	id: "job-1",
	queue: "sync",
	topic: "Repository sync",
	source: "accounts",
	scope: { global: true },
	state: "JOB_STATE_DEAD_LETTER",
	attemptCount: 5,
	maxAttempts: 5,
	createdAt: "2026-01-02T03:04:05Z",
	lastFailure: { code: RAW_CODE, message: RAW_MESSAGE },
};

function queuesAnswer(queues: unknown[]) {
	return http.post(admin("GetJobOperations"), () =>
		HttpResponse.json({ queues }),
	);
}
function jobsAnswer(jobs: unknown[], nextPageToken = "") {
	return http.post(admin("ListJobs"), () =>
		HttpResponse.json({ jobs, nextPageToken }),
	);
}
function refusal(method: string) {
	return http.post(admin(method), () =>
		HttpResponse.json(
			{ code: "permission_denied", message: "platform:jobs:read required" },
			{ status: 403 },
		),
	);
}
function outage(method: string) {
	return http.post(admin(method), () =>
		HttpResponse.json(
			{ code: "internal", message: "pq: connection refused" },
			{ status: 500 },
		),
	);
}

beforeEach(() => {
	toast.success.mockClear();
	toast.error.mockClear();
});
afterEach(cleanup);

describe("JobOperationsPage admin container", () => {
	it("renders queue snapshots the platform-admin service returns", async () => {
		server.use(queuesAnswer([EMAILS]), jobsAnswer([]));
		renderInApp(<JobOperationsPage />);
		expect(await screen.findByText("emails")).toBeTruthy();
		// Column headers are words, in sentence case.
		expect(
			screen.getByText("Waiting to start", { selector: "th *" }),
		).toBeTruthy();
		expect(
			screen.getByText("Stalled mid-run", { selector: "th *" }),
		).toBeTruthy();
	});

	it("shows a loading table, and no empty state, while the reads are in flight", async () => {
		server.use(
			http.post(admin("GetJobOperations"), () => delay("infinite")),
			http.post(admin("ListJobs"), () => delay("infinite")),
		);
		renderInApp(<JobOperationsPage />);
		await waitFor(() =>
			expect(screen.getAllByLabelText("Loading table")).toHaveLength(2),
		);
		expect(screen.queryByText("No queues yet")).toBeNull();
		expect(screen.queryByText("No jobs yet")).toBeNull();
	});

	it("says what a queue and a job are when there are none", async () => {
		server.use(queuesAnswer([]), jobsAnswer([]));
		renderInApp(<JobOperationsPage />);
		expect(await screen.findByText("No queues yet")).toBeTruthy();
		expect(await screen.findByText("No jobs yet")).toBeTruthy();
		expect(
			screen.getByText(/A job is one piece of background work/),
		).toBeTruthy();
		// Zero queues is an answer, so the tiles count zero rather than dash.
		expect(screen.queryByText("—")).toBeNull();
	});

	it("says who can grant access when the reads are refused, and offers no retry", async () => {
		server.use(refusal("GetJobOperations"), refusal("ListJobs"));
		renderInApp(<JobOperationsPage />);
		expect(await screen.findByText("You can't see the queues")).toBeTruthy();
		expect(await screen.findByText("You can't see the jobs")).toBeTruthy();
		expect(
			screen.getAllByText(/Ask a platform super administrator to grant it/),
		).toHaveLength(2);
		expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
		expect(screen.queryByText(/platform:jobs:read/)).toBeNull();
		expect(screen.queryByText("No jobs yet")).toBeNull();
	});

	it("offers Retry after a real failure, without the server's text, and recovers", async () => {
		server.use(outage("GetJobOperations"), outage("ListJobs"));
		renderInApp(<JobOperationsPage />);
		expect(await screen.findByText("Couldn't load the queues")).toBeTruthy();
		expect(await screen.findByText("Couldn't load the jobs")).toBeTruthy();
		expect(screen.queryByText(/connection refused/)).toBeNull();
		expect(screen.queryByText("No queues yet")).toBeNull();

		server.use(queuesAnswer([EMAILS]), jobsAnswer([]));
		fireEvent.click(screen.getAllByRole("button", { name: "Retry" })[0]);
		expect(await screen.findByText("emails")).toBeTruthy();
	});

	it("puts a failure in plain words in the jobs table, never its code or message", async () => {
		server.use(queuesAnswer([EMAILS]), jobsAnswer([FAILED_JOB]));
		renderInApp(<JobOperationsPage />);
		expect(
			await screen.findByText(
				"Syncing a data source was stopped by a usage limit",
			),
		).toBeTruthy();
		expect(screen.getByText("5 of 5")).toBeTruthy();
		expect(screen.getByText("Whole platform")).toBeTruthy();
		expect(document.body.textContent).not.toContain(RAW_CODE);
		expect(document.body.textContent).not.toContain("rate limit");
		expect(document.body.textContent).not.toContain("DEAD_LETTER");
	});

	it("offers to clear filters that match nothing", async () => {
		const asked: unknown[] = [];
		server.use(
			queuesAnswer([EMAILS]),
			http.post(admin("ListJobs"), async ({ request }) => {
				const body = (await request.json()) as { queue?: string };
				asked.push(body.queue ?? "");
				return HttpResponse.json({ jobs: body.queue ? [] : [FAILED_JOB] });
			}),
		);
		renderInApp(<JobOperationsPage />);
		await screen.findByText("Repository sync");
		fireEvent.change(screen.getByLabelText("Queue"), {
			target: { value: " nope " },
		});
		fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
		expect(await screen.findByText("No jobs match these filters")).toBeTruthy();
		expect(asked).toContain("nope");
		fireEvent.click(
			within(
				screen.getByText("No jobs match these filters").parentElement!,
			).getByRole("button", { name: "Clear filters" }),
		);
		expect(await screen.findByText("Repository sync")).toBeTruthy();
		expect((screen.getByLabelText("Queue") as HTMLInputElement).value).toBe("");
	});

	it("pages through the jobs with the server's page tokens", async () => {
		const tokens: string[] = [];
		server.use(
			queuesAnswer([EMAILS]),
			http.post(admin("ListJobs"), async ({ request }) => {
				const body = (await request.json()) as { pageToken?: string };
				tokens.push(body.pageToken ?? "");
				return body.pageToken === "t2"
					? HttpResponse.json({
							jobs: [{ ...FAILED_JOB, id: "job-2", topic: "Second page job" }],
						})
					: HttpResponse.json({ jobs: [FAILED_JOB], nextPageToken: "t2" });
			}),
		);
		renderInApp(<JobOperationsPage />);
		await screen.findByText("Repository sync");
		expect(screen.getByText("Page 1")).toBeTruthy();
		expect(
			(screen.getByRole("button", { name: "Previous" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);

		fireEvent.click(screen.getByRole("button", { name: "Next" }));
		expect(await screen.findByText("Second page job")).toBeTruthy();
		expect(screen.getByText("Page 2")).toBeTruthy();
		expect(tokens).toEqual(["", "t2"]);
		expect(
			(screen.getByRole("button", { name: "Next" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);

		fireEvent.click(screen.getByRole("button", { name: "Previous" }));
		expect(await screen.findByText("Repository sync")).toBeTruthy();
		expect(screen.getByText("Page 1")).toBeTruthy();
	});
});

describe("JobOperationsPage job details", () => {
	function withJobDetail() {
		server.use(
			queuesAnswer([EMAILS]),
			jobsAnswer([FAILED_JOB]),
			http.post(admin("GetJob"), () =>
				HttpResponse.json({
					job: FAILED_JOB,
					attempts: [
						{
							id: "attempt-1",
							jobId: "job-1",
							number: 1,
							workerId: "worker-7f9c",
							outcome: "JOB_ATTEMPT_OUTCOME_PERMANENT_FAILURE",
							failure: { code: RAW_CODE, message: RAW_MESSAGE },
							startedAt: "2026-01-02T03:05:00Z",
							finishedAt: "2026-01-02T03:05:02Z",
						},
					],
					transitions: [
						{
							sequence: "1",
							jobId: "job-1",
							toState: "JOB_STATE_PENDING",
							occurredAt: "2026-01-02T03:04:05Z",
						},
						{
							sequence: "2",
							jobId: "job-1",
							fromState: "JOB_STATE_PROCESSING",
							toState: "JOB_STATE_DEAD_LETTER",
							failure: { code: RAW_CODE, message: RAW_MESSAGE },
							occurredAt: "2026-01-02T03:05:02Z",
						},
					],
				}),
			),
		);
	}

	async function openDetails() {
		renderInApp(<JobOperationsPage />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: /View details of the Repository sync job/,
			}),
		);
		return screen.findByRole("dialog");
	}

	it("explains the failure, whether it retries and what to do, without the raw record", async () => {
		withJobDetail();
		const dialog = await openDetails();
		expect(
			await within(dialog).findByText(
				"Syncing a data source was stopped by a usage limit.",
			),
		).toBeTruthy();
		expect(
			within(dialog).getByText(/it will not be tried again on its own/),
		).toBeTruthy();
		expect(
			within(dialog).getByText(/Wait for the limit to reset/),
		).toBeTruthy();
		// The tries and the history are tables of words.
		expect(within(dialog).getByText("Created as pending")).toBeTruthy();
		expect(
			within(dialog).getByText("Processing to failed for good"),
		).toBeTruthy();
		expect(
			within(dialog).getAllByText("Failed for good").length,
		).toBeGreaterThan(0);
		expect(dialog.textContent).not.toContain(RAW_CODE);
		expect(dialog.textContent).not.toContain("rate limit");
		expect(dialog.textContent).not.toMatch(/DEAD_LETTER|PERMANENT_FAILURE/);
	});

	it("says a job could not be loaded instead of showing an empty record", async () => {
		server.use(
			queuesAnswer([EMAILS]),
			jobsAnswer([FAILED_JOB]),
			outage("GetJob"),
		);
		const dialog = await openDetails();
		expect(
			await within(dialog).findByText("Couldn't load this job"),
		).toBeTruthy();
		expect(within(dialog).getByRole("button", { name: "Retry" })).toBeTruthy();
		expect(
			within(dialog).queryByRole("button", { name: "Run this job again" }),
		).toBeNull();
	});

	it("asks before running a failed job again, naming the job, and queues it on confirm", async () => {
		withJobDetail();
		const replayed: unknown[] = [];
		server.use(
			http.post(admin("ReplayJob"), async ({ request }) => {
				replayed.push(await request.json());
				return HttpResponse.json({ jobId: "job-9" });
			}),
		);
		const dialog = await openDetails();
		fireEvent.click(
			await within(dialog).findByRole("button", { name: "Run this job again" }),
		);
		const question = await screen.findByRole("alertdialog");
		expect(
			within(question).getByText(
				/^Run the Repository sync job from .* again\?$/,
			),
		).toBeTruthy();
		expect(
			within(question).getByText(/a queued job cannot be taken back/),
		).toBeTruthy();
		// Nothing is sent until the question is answered.
		expect(replayed).toEqual([]);

		fireEvent.click(
			within(question).getByRole("button", { name: "Run job again" }),
		);
		await waitFor(() => expect(toast.success).toHaveBeenCalled());
		expect(replayed).toHaveLength(1);
		expect((replayed[0] as { sourceJobId: string }).sourceJobId).toBe("job-1");
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("leaves the job alone when the question is cancelled", async () => {
		withJobDetail();
		const replayed: unknown[] = [];
		server.use(
			http.post(admin("ReplayJob"), async ({ request }) => {
				replayed.push(await request.json());
				return HttpResponse.json({ jobId: "job-9" });
			}),
		);
		const dialog = await openDetails();
		fireEvent.click(
			await within(dialog).findByRole("button", { name: "Run this job again" }),
		);
		const question = await screen.findByRole("alertdialog");
		fireEvent.click(within(question).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(replayed).toEqual([]);
	});

	it("reports a refused re-run in plain words and keeps the question open", async () => {
		withJobDetail();
		server.use(outage("ReplayJob"));
		const dialog = await openDetails();
		fireEvent.click(
			await within(dialog).findByRole("button", { name: "Run this job again" }),
		);
		const question = await screen.findByRole("alertdialog");
		fireEvent.click(
			within(question).getByRole("button", { name: "Run job again" }),
		);
		await waitFor(() => expect(toast.error).toHaveBeenCalled());
		expect(toast.error.mock.calls[0][0]).toBe(
			"Couldn't run the job again. Try again; if it keeps failing, tell your administrator.",
		);
		expect(screen.getByRole("alertdialog")).toBeTruthy();
	});
	it("keeps failure details private in the list and inspection dialog", async () => {
		const job = {
			id: "job-1",
			queue: "emails",
			state: "JOB_STATE_DEAD_LETTER",
			attemptCount: 1,
			maxAttempts: 1,
			lastFailure: {
				code: "PRIVATE_INTERNAL_CODE",
				message: "secret-token-123",
			},
		};
		server.use(
			http.post(rpc("PlatformAdminService", "GetJobOperations"), () =>
				HttpResponse.json({ queues: [] }),
			),
			http.post(rpc("PlatformAdminService", "ListJobs"), () =>
				HttpResponse.json({ jobs: [job] }),
			),
			http.post(rpc("PlatformAdminService", "GetJob"), () =>
				HttpResponse.json({ job, attempts: [], transitions: [] }),
			),
		);
		renderInApp(<JobOperationsPage />);
		expect(await screen.findByText("The job failed for a reason this page has no description for")).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: /View details of/ }));
		expect(await screen.findByText(/Tell the team that owns this work/)).toBeTruthy();
		expect(
			screen.queryByText(/PRIVATE_INTERNAL_CODE|secret-token-123/),
		).toBeNull();
	});
});
