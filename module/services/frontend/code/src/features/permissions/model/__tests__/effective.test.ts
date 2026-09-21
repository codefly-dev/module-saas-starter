import { describe, expect, it } from "vitest";
import { sourceKey } from "../../ui/grant-source";
import {
	grantCovers,
	groupByResource,
	parsePermission,
	resolveEffectivePermissions,
	rolesGranting,
	sourcesGranting,
} from "../effective";

const editor = {
	id: "role-editor",
	name: "editor",
	permissions: [
		{ resource: "users", action: "read" },
		{ resource: "users", action: "write" },
	],
};
const auditor = {
	id: "role-auditor",
	name: "auditor",
	permissions: [{ resource: "users", action: "read" }],
};
const superRole = {
	id: "role-super",
	name: "super",
	permissions: [{ resource: "*", action: "*" }],
};

describe("resolveEffectivePermissions", () => {
	it("traces a direct grant to the role that carries it", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [editor],
			assignments: [{ subjectId: "user-1", roleId: "role-editor" }],
			teams: [],
		});
		expect(effective.map((entry) => entry.permission)).toEqual([
			"users:read",
			"users:write",
		]);
		expect(effective[0].sources).toEqual([
			{ via: "direct", roleId: "role-editor", roleName: "editor", scope: "" },
		]);
	});

	it("traces a team grant to the team that carries it", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [auditor],
			assignments: [{ subjectId: "team-1", roleId: "role-auditor" }],
			teams: [{ id: "team-1", name: "compliance" }],
		});
		expect(effective[0].sources).toEqual([
			{
				via: "team",
				roleId: "role-auditor",
				roleName: "auditor",
				teamId: "team-1",
				teamName: "compliance",
				scope: "",
			},
		]);
	});

	// Revoking one of two paths to the same permission changes nothing, and an
	// administrator has to be able to see that before they try.
	it("collapses two paths to one permission into one row with both sources", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [editor, auditor],
			assignments: [
				{ subjectId: "user-1", roleId: "role-editor" },
				{ subjectId: "team-1", roleId: "role-auditor" },
			],
			teams: [{ id: "team-1", name: "compliance" }],
		});
		const read = effective.find((entry) => entry.permission === "users:read");
		expect(read?.sources.map((source) => source.via)).toEqual([
			"direct",
			"team",
		]);
	});

	it("ignores another subject's assignment and a team the subject is not in", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [editor, auditor],
			assignments: [
				{ subjectId: "user-2", roleId: "role-editor" },
				{ subjectId: "team-9", roleId: "role-auditor" },
			],
			teams: [{ id: "team-1", name: "compliance" }],
		});
		expect(effective).toEqual([]);
	});

	it("drops an assignment whose role is not visible rather than inventing one", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [],
			assignments: [{ subjectId: "user-1", roleId: "role-editor" }],
			teams: [],
		});
		expect(effective).toEqual([]);
	});
});

describe("grantCovers", () => {
	it("matches exactly, on either wildcard half, and on neither otherwise", () => {
		const wanted = { resource: "users", action: "read" };
		expect(grantCovers({ resource: "users", action: "read" }, wanted)).toBe(
			true,
		);
		expect(grantCovers({ resource: "*", action: "read" }, wanted)).toBe(true);
		expect(grantCovers({ resource: "users", action: "*" }, wanted)).toBe(true);
		expect(grantCovers({ resource: "*", action: "*" }, wanted)).toBe(true);
		expect(grantCovers({ resource: "users", action: "write" }, wanted)).toBe(
			false,
		);
		expect(grantCovers({ resource: "teams", action: "read" }, wanted)).toBe(
			false,
		);
	});
});

describe("sourcesGranting", () => {
	it("counts a wildcard grant as holding a permission it does not name", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [superRole],
			assignments: [{ subjectId: "user-1", roleId: "role-super" }],
			teams: [],
		});
		expect(effective.map((entry) => entry.permission)).toEqual(["*:*"]);
		expect(
			sourcesGranting({ resource: "webhooks", action: "write" }, effective),
		).toEqual([
			{ via: "direct", roleId: "role-super", roleName: "super", scope: "" },
		]);
	});

	it("reports no source when nothing grants it", () => {
		expect(
			sourcesGranting({ resource: "webhooks", action: "write" }, []),
		).toEqual([]);
	});
});

describe("rolesGranting", () => {
	it("returns every role whose grant covers the permission", () => {
		expect(
			rolesGranting({ resource: "users", action: "read" }, [
				editor,
				auditor,
				superRole,
			]).map((role) => role.name),
		).toEqual(["editor", "auditor", "super"]);
		expect(
			rolesGranting({ resource: "users", action: "write" }, [
				editor,
				auditor,
			]).map((role) => role.name),
		).toEqual(["editor"]);
	});
});

describe("parsePermission", () => {
	it("splits on the first colon and rejects a label with no two halves", () => {
		expect(parsePermission("users:read")).toEqual({
			resource: "users",
			action: "read",
		});
		expect(parsePermission("users")).toBeUndefined();
		expect(parsePermission(":read")).toBeUndefined();
		expect(parsePermission("users:")).toBeUndefined();
	});
});

describe("groupByResource", () => {
	it("groups the vocabulary by resource and sorts both levels", () => {
		expect(
			groupByResource(["users:write", "teams:read", "users:read", "bogus"]),
		).toEqual([
			{
				resource: "teams",
				permissions: [{ resource: "teams", action: "read" }],
			},
			{
				resource: "users",
				permissions: [
					{ resource: "users", action: "read" },
					{ resource: "users", action: "write" },
				],
			},
		]);
	});
});

// A scope-qualified assignment grants the role only there. Dropping the scope
// renders a narrow grant as organization-wide authority, and acting on it
// revokes nothing: the delete predicate matches on scope too.
describe("scope", () => {
	it("carries the assignment's scope onto every source it produces", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [editor],
			assignments: [
				{ subjectId: "user-1", roleId: "role-editor", scope: "project-x" },
			],
			teams: [],
		});
		expect(effective[0].sources).toEqual([
			{
				via: "direct",
				roleId: "role-editor",
				roleName: "editor",
				scope: "project-x",
			},
		]);
	});

	it("keeps an org-wide grant and a scoped grant of the same role apart", () => {
		const effective = resolveEffectivePermissions({
			subjectId: "user-1",
			roles: [editor],
			assignments: [
				{ subjectId: "user-1", roleId: "role-editor" },
				{ subjectId: "user-1", roleId: "role-editor", scope: "project-x" },
			],
			teams: [],
		});
		expect(effective[0].sources.map((source) => source.scope)).toEqual([
			"",
			"project-x",
		]);
		expect(new Set(effective[0].sources.map(sourceKey)).size).toBe(2);
	});
});
