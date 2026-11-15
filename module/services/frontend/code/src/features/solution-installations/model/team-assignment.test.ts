import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { TeamSchema } from "@/gen/saas/accounts/v1/common_pb";
import { teamAssignment, teamSlug } from "./team-assignment";

const team = (id: string, name: string, path: string, parentTeamId = "") =>
	create(TeamSchema, { id, name, path, parentTeamId });

describe("team assignment", () => {
	it("derives the slug as the host does", () => {
		expect(teamSlug("Platform Eng.")).toBe("platform-eng");
		expect(teamSlug("example-user")).toBe("example-user");
	});
	it("reuses a team holding the conventional slug under another name", () => {
		const assignment = teamAssignment(
			"example",
			["user", "admin"],
			[
				team("t-user", "Example User", "example-user"),
				team("t-nested", "example-admin", "parent/example-admin", "t-parent"),
			],
			{},
		);
		expect(assignment[0]).toMatchObject({
			role: "user",
			name: "Example User",
			chosen: false,
		});
		expect(assignment[0].team?.id).toBe("t-user");
		// A nested team is matched by its name, never its path.
		expect(assignment[1].team?.id).toBe("t-nested");
	});
	it("creates the conventional team when no root team holds its name or slug", () => {
		const [user] = teamAssignment(
			"example",
			["user"],
			[team("t-other", "Readers", "parent/example-user", "t-parent")],
			{},
		);
		expect(user).toMatchObject({ name: "example-user", team: undefined });
	});
});
