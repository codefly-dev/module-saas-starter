import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { JobOperationsPage } from "./job-operations-page";

// Both list queries route through PlatformAdminService (saas.accounts.v1),
// even though the message types live in saas.jobs.v1.
afterEach(cleanup);

describe("JobOperationsPage admin container", () => {
	it("renders queue snapshots the platform-admin service returns", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetJobOperations"), () =>
				HttpResponse.json({
					queues: [
						{
							queue: "emails",
							ready: "5",
							processing: "2",
							scheduled: "1",
							deadLetter: "0",
							expiredLeases: "0",
						},
					],
				}),
			),
			http.post(rpc("PlatformAdminService", "ListJobs"), () =>
				HttpResponse.json({ jobs: [], nextPageToken: "" }),
			),
		);
		renderInApp(<JobOperationsPage />);
		expect(await screen.findByText("emails")).toBeTruthy();
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
		expect(await screen.findByText("Attempt failed")).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Inspect" }));
		expect(await screen.findByText(/The job could not finish/)).toBeTruthy();
		expect(
			screen.queryByText(/PRIVATE_INTERNAL_CODE|secret-token-123/),
		).toBeNull();
	});
});
