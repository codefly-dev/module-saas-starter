import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { TeamSchema } from "@/gen/saas/accounts/v1/common_pb";
import { teamAssignment, teamSlug } from "./team-assignment";

const team = (id: string, name: string, path: string, parentTeamId = "") =>
	create(TeamSchema, { id, name, path, parentTeamId });

describe("team assignment", () => {
	it("derives the slug as the host does", () => {
		expect(teamSlug("Platform Eng.")).toBe("platform-eng");
		expect(teamSlug("example-viewer")).toBe("example-viewer");
	});
	it("reuses a team holding the conventional slug under another name", () => {
		const assignment = teamAssignment(
			"example",
			["viewer", "editor"],
			[
				team("t-viewer", "Example Viewer", "example-viewer"),
				team("t-nested", "example-editor", "parent/example-editor", "t-parent"),
			],
			{},
		);
		expect(assignment[0]).toMatchObject({
			role: "viewer",
			name: "Example Viewer",
			chosen: false,
		});
		expect(assignment[0].team?.id).toBe("t-viewer");
		// A nested team is matched by its name, never its path.
		expect(assignment[1].team?.id).toBe("t-nested");
	});
	it("creates the conventional team when no root team holds its name or slug", () => {
		const [viewer] = teamAssignment(
			"example",
			["viewer"],
			[team("t-other", "Readers", "parent/example-viewer", "t-parent")],
			{},
		);
		expect(viewer).toMatchObject({ name: "example-viewer", team: undefined });
	});
});
