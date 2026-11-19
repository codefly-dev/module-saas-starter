import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { TeamPeople } from "./team-people";

const members = vi.hoisted(() => ({
	list: vi.fn(),
	remove: vi.fn(),
}));
vi.mock("@/features/teams/service/queries", () => ({
	teamQueries: {
		members: (teamId: string) => ({
			queryKey: ["team-members", teamId],
			queryFn: members.list,
		}),
	},
}));
vi.mock("@/features/teams/service/mutations", () => ({
	teamMutations: { addMember: vi.fn(), removeMember: members.remove },
}));
vi.mock("@/components/user-picker", () => ({ UserPicker: () => null }));

const team = { kind: "team" as const, id: "team-1", label: "Finance" };

beforeEach(() => {
	vi.clearAllMocks();
	members.list.mockResolvedValue({
		members: [{ userId: "user-1", userEmail: "jane@example.com" }],
	});
	members.remove.mockResolvedValue({});
});
afterEach(cleanup);

async function askToRemove() {
	renderInApp(<TeamPeople orgId="org-1" team={team} />);
	fireEvent.click(
		await screen.findByRole("button", {
			name: "Add or remove people in Finance",
		}),
	);
	fireEvent.click(
		await screen.findByRole("button", {
			name: "Remove jane@example.com from Finance",
		}),
	);
	return screen.findByRole("alertdialog");
}

describe("removing a person from a team that holds a role", () => {
	it("asks first, naming the person and the team and what they lose", async () => {
		const question = await askToRemove();
		expect(
			within(question).getByText("Remove jane@example.com from Finance?"),
		).toBeTruthy();
		expect(
			within(question).getByText(/They lose every role Finance has at once/),
		).toBeTruthy();
		expect(members.remove).not.toHaveBeenCalled();
		fireEvent.click(
			within(question).getByRole("button", { name: "Remove from team" }),
		);
		await waitFor(() =>
			expect(members.remove).toHaveBeenCalledExactlyOnceWith(
				"team-1",
				"user-1",
			),
		);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("removes nobody on Cancel", async () => {
		const question = await askToRemove();
		fireEvent.click(within(question).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(members.remove).not.toHaveBeenCalled();
		expect(
			within(screen.getByRole("list", { name: "People in Finance" })).getByText(
				"jane@example.com",
			),
		).toBeTruthy();
	});
});
