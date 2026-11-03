import { describe, expect, it } from "vitest";
import { accessLevel, accessMix, byAccess, nameList } from "../setup/model.js";

const scope = (resourceKind: string, ...actions: string[]) =>
	({
		resourceKind,
		actions,
		attachedOnly: false,
		resourceIds: [],
	}) as never;

describe("the access one operation demands", () => {
	it("names a read-only operation read", () => {
		expect(accessLevel([scope("documents", "read")])).toBe("read");
		expect(
			accessLevel([
				scope("documents", "read"),
				scope("documents.collection", "read"),
			]),
		).toBe("read");
	});

	// The number a reader came for is how many operations CHANGE something. An
	// operation that writes and also reads is a write; filing it under
	// "read, write" would split the write count over two buckets.
	it("names a write that also reads a write, not both", () => {
		expect(
			accessLevel([
				scope("documents", "write"),
				scope("documents.collection", "read"),
			]),
		).toBe("write");
	});

	it("lists every changing action a module declares, without ranking them", () => {
		expect(accessLevel([scope("wiki.page", "publish", "create")])).toBe(
			"create, publish",
		);
	});

	it("does not call an operation with no scope a read", () => {
		expect(accessLevel([])).toBe("no scope");
	});

	it("counts the mix, reads first and the unscoped last", () => {
		expect(
			accessMix([
				[scope("documents", "read")],
				[scope("documents", "read")],
				[scope("documents", "write")],
				[],
			]),
		).toBe("2 read, 1 write, 1 no scope");
	});

	it("orders access labels with the read floor first", () => {
		expect(["write", "no scope", "read", "publish"].sort(byAccess)).toEqual([
			"read",
			"publish",
			"write",
			"no scope",
		]);
	});

	it("names the roles that coincide as a sentence would", () => {
		expect(nameList([])).toBe("");
		expect(nameList(["editor"])).toBe("editor");
		expect(nameList(["editor", "installer"])).toBe("editor and installer");
		expect(nameList(["a", "b", "c"])).toBe("a, b and c");
	});
});
