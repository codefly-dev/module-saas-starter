import { cleanup, screen } from "@testing-library/react";
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
});
