import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { UsersPage } from "./users-page";

afterEach(cleanup);

describe("UsersPage admin container", () => {
	it("renders the users the platform admin service returns", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "SearchUsers"), () =>
				HttpResponse.json({
					users: [
						{
							uuid: "user-1",
							primaryEmail: "admin@acme.test",
							status: 1,
							emailVerified: true,
							profile: {},
						},
					],
				}),
			),
		);
		renderInApp(<UsersPage />);
		// Email shows in the Email column and again as the Name fallback (empty
		// profile), so match all occurrences rather than a single node.
		expect(
			(await screen.findAllByText("admin@acme.test")).length,
		).toBeGreaterThan(0);
	});
});
