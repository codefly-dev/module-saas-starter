import { describe, expect, it } from "vitest";
import {
	type AbilityReach,
	type AbilityRole,
	kindText,
	operationText,
	roleAbilities,
	scopesText,
	unreachedHeadline,
	unreachedText,
	updateAsks,
} from "../setup/abilities.js";

const scope = (resourceKind: string, ...actions: string[]) => ({
	resourceKind,
	actions,
});
const row = (
	path: string[],
	via: string,
	scopes: ReturnType<typeof scope>[],
	extra: Partial<AbilityReach> = {},
): AbilityReach => ({
	path,
	module: path[path.length - 1],
	via,
	confinement: "",
	scopes,
	operations: [],
	...extra,
});

// A solution whose viewer reads pages, reaches documents through the
// solution and the model's profiles through the solution and an agent, and
// whose page reaches the directory on the person's own session.
const viewer: AbilityRole = {
	name: "viewer",
	scopes: [scope("pages.page", "read")],
	reach: [
		row(["pages"], "role", [scope("pages.page", "read")], {
			confinement: "installation",
		}),
		row(["pages", "documents"], "binding", [
			scope("documents.document", "read"),
		]),
		row(["pages", "agent", "model"], "binding", [
			scope("model.profile", "invoke"),
		]),
		row(["pages", "directory"], "person", []),
	],
};

describe("what a role can do", () => {
	it("groups a role's reach by where, in plain words, through every hop", () => {
		const [abilities] = roleAbilities("pages", [viewer]);
		expect(abilities.role).toBe("viewer");
		expect(abilities.groups.map((g) => [g.where, g.lines])).toEqual([
			["In pages", ["read pages"]],
			["Through pages, in documents", ["read documents"]],
			["Through pages and agent, in model", ["use profiles"]],
			[
				"With their own access in directory",
				["only what they already hold there themselves"],
			],
		]);
	});

	it("never shows an identifier: kinds, verbs and operations read as words", () => {
		expect(kindText("documents.collection")).toBe("collections");
		expect(kindText("example.policy")).toBe("policies");
		expect(scopesText([scope("pages.page", "read", "write", "admin")])).toBe(
			"read, edit and manage pages",
		);
		expect(operationText("/pkg.v1.Items/ListItems")).toBe("list items");
		expect(operationText("GET /items/{id}")).toBe("get items");
	});

	it("names a row's operations when it carries no scope, and what is attached", () => {
		const [abilities] = roleAbilities("pages", [
			{
				name: "editor",
				scopes: [],
				reach: [
					row(["pages"], "role", [scope("pages.page", "read")], {
						confinement: "attached",
					}),
					row(["pages", "search"], "binding", [], {
						operations: ["/s.v1.Search/Query", "/s.v1.Search/Suggest"],
					}),
				],
			},
		]);
		expect(abilities.groups.map((g) => g.lines)).toEqual([
			["read pages, only those attached to it"],
			["query and suggest"],
		]);
	});

	it("reads a role's own scopes when the offer carries no reach", () => {
		const [abilities] = roleAbilities("pages", [
			{ name: "viewer", scopes: [scope("pages.page", "read")], reach: [] },
		]);
		expect(abilities.groups).toEqual([
			{ key: "role", where: "In pages", lines: ["read pages"] },
		]);
	});
});

describe("what an update asks", () => {
	it("names what each role reaches beyond the approved reach, and nothing else", () => {
		const editor: AbilityRole = {
			name: "editor",
			scopes: [],
			reach: [
				row(["pages", "agent", "documents"], "binding", [
					scope("documents.collection", "read", "create"),
				]),
			],
		};
		const asks = updateAsks(
			"pages",
			[viewer, editor],
			[
				viewer,
				{
					name: "editor",
					scopes: [],
					reach: [
						row(["pages", "agent", "documents"], "binding", [
							scope("documents.collection", "read"),
						]),
					],
				},
			],
		);
		expect(asks).toEqual([
			{
				role: "editor",
				groups: [
					{
						key: "binding:pages>agent>documents",
						where: "Through pages and agent, in documents",
						lines: ["create collections"],
					},
				],
			},
		]);
	});

	it("names a new role, a new place, and an operation newly open to what was held", () => {
		const approved: AbilityRole = {
			...viewer,
			reach: [
				row(
					["pages", "documents"],
					"binding",
					[scope("documents.document", "read")],
					{
						operations: ["/d.v1.Docs/Get"],
					},
				),
			],
		};
		const now: AbilityRole = {
			...viewer,
			reach: [
				row(
					["pages", "documents"],
					"binding",
					[scope("documents.document", "read")],
					{
						operations: ["/d.v1.Docs/Get", "/d.v1.Docs/Purge"],
					},
				),
				row(["pages", "model"], "binding", [scope("model.profile", "invoke")]),
			],
		};
		const asks = updateAsks(
			"pages",
			[
				now,
				{ name: "auditor", scopes: [scope("pages.page", "read")], reach: [] },
			],
			[approved],
		);
		expect(
			asks.map((a) => [a.role, a.groups.map((g) => [g.where, g.lines])]),
		).toEqual([
			[
				"viewer",
				[
					["Through pages, in documents", ["purge"]],
					["Through pages, in model", ["use profiles"]],
				],
			],
			["auditor", [["In pages", ["read pages"]]]],
		]);
	});

	it("asks nothing when the update only narrows", () => {
		expect(updateAsks("pages", [{ ...viewer, reach: [] }], [viewer])).toEqual(
			[],
		);
	});
});

describe("an unreached reach", () => {
	it("reads the core's condition in words, and keeps any other form as written", () => {
		expect(
			unreachedText(
				"role viewer reaches nothing of model.profile:invoke, model.profile:read through agent: no module agent reaches consumes it for a person",
			),
		).toBe(
			"A viewer can't use and read profiles through agent right now: agent no longer passes it on.",
		);
		expect(
			unreachedText(
				"role editor reaches nothing of documents.document:write through agent: documents not composed",
			),
		).toBe(
			"An editor can't edit documents through agent right now: documents is not available here.",
		);
		expect(unreachedText("something else")).toBe("something else");
	});

	it("names on one line what cannot be reached, by module and the binding it goes through", () => {
		expect(
			unreachedHeadline([
				"role viewer reaches nothing of model.profile:invoke through agent: agent not composed",
				"role editor reaches nothing of model.profile:invoke, documents.document:write through agent: no module agent reaches consumes it for a person",
			]),
		).toBe("Can't reach model and documents through agent");
		expect(unreachedHeadline(["other"])).toBe(
			"Some access can't be reached right now",
		);
	});
});
