import { describe, expect, it } from "vitest";
import {
	groupSolutionNavigation,
	parseSolutionGroup,
} from "../solution-groups";

const group = { id: "operations", title: "Operations", order: 2 };
describe("solution groups", () => {
	it("accepts presentation only and refuses authority-shaped or malformed metadata", () => {
		expect(parseSolutionGroup(undefined)).toBeUndefined();
		expect(parseSolutionGroup(group)).toEqual(group);
		for (const input of [
			null,
			[],
			{ ...group, grants: [] },
			{ ...group, id: "../other" },
			{ ...group, title: "" },
			{ ...group, title: "bad\nname" },
			{ ...group, title: "bad\u0085name" },
			{ ...group, order: 0.5 },
			{ ...group, order: 2147483648 },
			{ ...group, order: -2147483649 },
			{ ...group, title: "界".repeat(342) },
			{ ...group, id: "a".repeat(1025) },
		]) {
			expect(parseSolutionGroup(input)).toBeNull();
		}
	});
	it("uses the producer's UTF-8 bound and exact signed integer range", () => {
		expect(
			parseSolutionGroup({
				...group,
				title: "界".repeat(341),
				order: 2147483647,
			}),
		).not.toBeNull();
		expect(parseSolutionGroup({ ...group, order: -2147483648 })).not.toBeNull();
	});
	it("orders visible groups, preserves solution entries, and has no empty groups", () => {
		const a = { id: "a", group };
		const b = {
			id: "b",
			group: { id: "research", title: "Research", order: 1 },
		};
		const c: { id: string; group?: typeof group } = { id: "c" };
		expect(groupSolutionNavigation([a, b, c])).toEqual([
			{ key: "group:research", title: "Research", items: [b] },
			{ key: "group:operations", title: "Operations", items: [a] },
			{ key: "ungrouped", title: "Other", items: [c] },
		]);
		expect(groupSolutionNavigation([])).toEqual([]);
		expect(groupSolutionNavigation([c])).toEqual([
			{ key: "ungrouped", title: "Solutions", items: [c] },
		]);
	});
});
