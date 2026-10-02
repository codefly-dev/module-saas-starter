import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { addMemberErrorMessage, TeamMembersPanel } from "./team-members-panel";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-example", user: { id: "u-admin" } }),
}));
afterEach(cleanup);

describe("addMemberErrorMessage", () => {
	it("shows the server's own words when the target is ineligible", () => {
		expect(
			addMemberErrorMessage(
				new ConnectError(
					"user is not a member of the team's organization",
					Code.FailedPrecondition,
				),
			),
		).toBe("user is not a member of the team's organization");
	});

	it("does not put server internals in front of the user", () => {
		expect(
			addMemberErrorMessage(
				new ConnectError(
					'failed to add team member: pq: connection refused "10.0.0.4:5432"',
					Code.Internal,
				),
			),
		).toBe("Failed to add member");
	});

	it("falls back for an error that never reached the server", () => {
		expect(addMemberErrorMessage(new Error("network down"))).toBe(
			"Failed to add member",
		);
	});
});

describe("adding a member reports the write, not the read that follows it", () => {
	// The roster read is held open after the write lands. Awaiting the
	// invalidation inside `onSuccess` kept the mutation `isPending` for exactly
	// this long, which is how "Adding…" and a stale count outlived a membership
	// that had already been written (#964).
	async function addAMemberWithTheRosterHeld() {
		let releaseRoster: () => void = () => {};
		let rosterCalls = 0;
		server.use(
			http.post(rpc("OrganizationService", "ListMembers"), () =>
				HttpResponse.json({
					members: [{ userId: "u-new", userEmail: "newbie@example.com" }],
				}),
			),
			http.post(rpc("TeamService", "AddMember"), () => HttpResponse.json({})),
			http.post(rpc("TeamService", "ListMembers"), async () => {
				rosterCalls += 1;
				if (rosterCalls > 1) {
					await new Promise<void>((resolve) => {
						releaseRoster = resolve;
					});
				}
				return HttpResponse.json({
					members:
						rosterCalls > 1
							? [
									{
										teamId: "team-example",
										userId: "u-new",
										userEmail: "newbie@example.com",
										role: 1,
									},
								]
							: [],
				});
			}),
		);
		renderInApp(
			<TeamMembersPanel
				orgId="org-example"
				teamId="team-example"
				teamName="Example team"
				canManage
				currentUserId="u-admin"
			/>,
		);
		fireEvent.change(
			await screen.findByPlaceholderText("Search members by email…"),
			{ target: { value: "newbie" } },
		);
		fireEvent.click(
			await screen.findByRole("button", { name: "newbie@example.com" }),
		);
		fireEvent.click(await screen.findByRole("button", { name: "Add member" }));
		await waitFor(() => expect(rosterCalls).toBeGreaterThan(1));
		return () => releaseRoster();
	}

	it("settles the button while the roster is still being re-read", async () => {
		const release = await addAMemberWithTheRosterHeld();
		try {
			// The write has landed and the refresh is still in flight. The button
			// must be back to offering the next add, not reporting the read.
			await waitFor(() =>
				expect(screen.queryByRole("button", { name: "Adding…" })).toBeNull(),
			);
			expect(screen.getByRole("button", { name: "Add member" })).toBeTruthy();
		} finally {
			release();
		}
	});

	// Settling the button is only half of it: the heading still reads the CACHED
	// roster, which after a write is behind the database until the refresh lands.
	// That is the "(0)" half of #964, and it must not sit there mute.
	it("says the roster is being refreshed rather than letting a stale count stand mute", async () => {
		const release = await addAMemberWithTheRosterHeld();
		try {
			await waitFor(() =>
				expect(screen.queryByRole("button", { name: "Adding…" })).toBeNull(),
			);
			// Gated by the same 200/300 rule as every other indicator, so a refresh
			// that lands quickly says nothing at all.
			expect(
				await screen.findByRole("status", { name: "Refreshing members" }),
			).toBeTruthy();
		} finally {
			release();
		}
	});

	it("still lands the new member once that re-read returns", async () => {
		const release = await addAMemberWithTheRosterHeld();
		release();
		expect(
			await screen.findByRole("heading", { name: /Members \(1\)/ }),
		).toBeTruthy();
		expect(screen.getByText("newbie@example.com")).toBeTruthy();
	});
});
