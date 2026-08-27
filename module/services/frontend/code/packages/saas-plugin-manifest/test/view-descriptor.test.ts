import { describe, expect, it } from "vitest";

import {
	assertViewDescriptor,
	assertViewOverride,
	FACET_KIND_HINTS,
	resolveViewDescriptor,
	type ViewDescriptor,
	type ViewOverride,
} from "../src/index.js";

function view(): ViewDescriptor {
	return {
		id: "documents",
		type: "table",
		facets: [
			{ facet: "title", kind: "text", filter: true },
			{ facet: "created", kind: "date", sort: "desc" },
			{ facet: "status", kind: "enum", badge: true, groupBy: true },
			{ facet: "owner", kind: "user", icon: "person" },
		],
	};
}

const rec = (value: unknown): Record<string, unknown> =>
	value as Record<string, unknown>;
const arr = (value: unknown): Record<string, unknown>[] =>
	value as Record<string, unknown>[];

// The parsed descriptor is untyped at the boundary. Each mutation breaks exactly
// one rule on a deep clone of the reference view, so one valid baseline drives
// every negative case.
function mutated(mutate: (v: Record<string, unknown>) => void): unknown {
	const clone = JSON.parse(JSON.stringify(view())) as Record<string, unknown>;
	mutate(clone);
	return clone;
}

const facet = (v: Record<string, unknown>, index: number) =>
	rec(arr(v.facets)[index]);

describe("assertViewDescriptor", () => {
	it("accepts a well-formed view", () => {
		expect(() => assertViewDescriptor(view())).not.toThrow();
	});

	it("rejects an unknown top-level field", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (v.layout = "grid"))),
		).toThrow(/unknown field 'layout'/);
	});

	it("rejects a non-object", () => {
		expect(() => assertViewDescriptor([])).toThrow(/must be an object/);
	});

	it("rejects an invalid id", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (v.id = "Documents"))),
		).toThrow(/view id 'Documents' is not a valid logical id/);
	});

	it("rejects an unsupported view type", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (v.type = "timeline"))),
		).toThrow(/type 'timeline' is unsupported/);
	});

	it("rejects an empty facet list", () => {
		expect(() => assertViewDescriptor(mutated((v) => (v.facets = [])))).toThrow(
			/must declare at least one facet/,
		);
	});

	it("rejects duplicate facets", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 1).facet = "title"))),
		).toThrow(/facet in view 'documents' 'title' is declared more than once/);
	});

	it("rejects an unknown facet kind", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 0).kind = "geo"))),
		).toThrow(/kind 'geo' is unsupported/);
	});

	it("rejects an unknown field on a rule", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 0).width = 10))),
		).toThrow(/unknown field 'width'/);
	});

	it("rejects a negative order", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 0).order = -1))),
		).toThrow(/order must be a non-negative integer/);
	});

	it("rejects a non-integer order", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 0).order = 1.5))),
		).toThrow(/order must be a non-negative integer/);
	});

	it("rejects an unsafe color token", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 0).color = "#fff"))),
		).toThrow(/color must be a token/);
	});

	it("rejects an unsupported sort direction", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 1).sort = "ascending"))),
		).toThrow(/sort must be 'asc' or 'desc'/);
	});

	it("rejects grouping by a non-groupable kind", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 0).groupBy = true))),
		).toThrow(/groups by a facet whose kind is not groupable/);
	});

	it("rejects sorting by a non-sortable kind", () => {
		expect(() =>
			assertViewDescriptor(mutated((v) => (facet(v, 2).sort = "asc"))),
		).toThrow(/sorts by a facet whose kind is not sortable/);
	});
});

describe("assertViewOverride", () => {
	it("accepts an empty override", () => {
		expect(() => assertViewOverride({})).not.toThrow();
	});

	it("accepts a type-only override", () => {
		expect(() => assertViewOverride({ type: "board" })).not.toThrow();
	});

	it("rejects an unknown field", () => {
		expect(() => assertViewOverride({ id: "x" })).toThrow(/unknown field 'id'/);
	});

	it("rejects a kind on a facet tweak", () => {
		expect(() =>
			assertViewOverride({ facets: [{ facet: "title", kind: "date" }] }),
		).toThrow(/unknown field 'kind'/);
	});

	it("rejects an unsupported type", () => {
		expect(() => assertViewOverride({ type: "timeline" })).toThrow(
			/type 'timeline' is unsupported/,
		);
	});

	it("rejects duplicate facet tweaks", () => {
		expect(() =>
			assertViewOverride({
				facets: [{ facet: "title" }, { facet: "title" }],
			}),
		).toThrow(/facet in view override 'title' is declared more than once/);
	});

	it("rejects an invalid sort direction in a tweak", () => {
		expect(() =>
			assertViewOverride({ facets: [{ facet: "created", sort: "sideways" }] }),
		).toThrow(/sort must be 'asc' or 'desc'/);
	});

	it("rejects a negative order in a tweak", () => {
		expect(() =>
			assertViewOverride({ facets: [{ facet: "created", order: -3 }] }),
		).toThrow(/order must be a non-negative integer/);
	});

	it("rejects an unsafe color token in a tweak", () => {
		expect(() =>
			assertViewOverride({
				facets: [{ facet: "status", color: "red; background:url(x)" }],
			}),
		).toThrow(/color must be a token/);
	});

	it("rejects a non-boolean badge in a tweak", () => {
		expect(() =>
			assertViewOverride({ facets: [{ facet: "status", badge: "yes" }] }),
		).toThrow(/badge must be a boolean/);
	});
});

describe("FACET_KIND_HINTS", () => {
	it("types a date facet as a date render", () => {
		expect(FACET_KIND_HINTS.date.render).toBe("date");
	});

	it("is frozen", () => {
		expect(Object.isFrozen(FACET_KIND_HINTS)).toBe(true);
	});
});

describe("resolveViewDescriptor", () => {
	it("resolves kind hints and rule fields with defaults", () => {
		const resolved = resolveViewDescriptor(view());
		expect(resolved.id).toBe("documents");
		expect(resolved.type).toBe("table");
		expect(resolved.facets.map((f) => f.facet)).toEqual([
			"title",
			"created",
			"status",
			"owner",
		]);
		const created = resolved.facets[1];
		expect(created).toMatchObject({
			facet: "created",
			kind: "date",
			render: "date",
			sortable: true,
			groupable: true,
			label: "created",
			column: true,
			order: 1,
			badge: false,
			groupBy: false,
			sort: "desc",
			filter: false,
		});
	});

	it("labels default to the facet name and can be overridden", () => {
		const resolved = resolveViewDescriptor(view(), {
			facets: [{ facet: "created", label: "Created at" }],
		});
		expect(resolved.facets[1].label).toBe("Created at");
	});

	it("lets a later layer win over an earlier one", () => {
		const prefs: ViewOverride = {
			type: "board",
			facets: [{ facet: "created", sort: "asc" }],
		};
		const skin: ViewOverride = {
			facets: [{ facet: "status", color: "amber", icon: "flag" }],
		};
		const resolved = resolveViewDescriptor(view(), prefs, skin);
		expect(resolved.type).toBe("board");
		expect(resolved.facets[1].sort).toBe("asc");
		const status = resolved.facets.find((f) => f.facet === "status");
		expect(status).toMatchObject({ color: "amber", icon: "flag" });
	});

	it("the last layer to set a field wins", () => {
		const resolved = resolveViewDescriptor(
			view(),
			{ facets: [{ facet: "title", label: "First" }] },
			{ facets: [{ facet: "title", label: "Second" }] },
		);
		expect(resolved.facets[0].label).toBe("Second");
	});

	it("reorders facets by the resolved order", () => {
		const resolved = resolveViewDescriptor(view(), {
			facets: [
				{ facet: "owner", order: 0 },
				{ facet: "title", order: 9 },
			],
		});
		expect(resolved.facets.map((f) => f.facet)).toEqual([
			"owner",
			"created",
			"status",
			"title",
		]);
	});

	it("keeps declaration order among facets sharing an order", () => {
		const resolved = resolveViewDescriptor(view(), {
			facets: [
				{ facet: "title", order: 5 },
				{ facet: "created", order: 5 },
				{ facet: "status", order: 5 },
				{ facet: "owner", order: 5 },
			],
		});
		expect(resolved.facets.map((f) => f.facet)).toEqual([
			"title",
			"created",
			"status",
			"owner",
		]);
	});

	it("rejects an override naming an unknown facet", () => {
		expect(() =>
			resolveViewDescriptor(view(), { facets: [{ facet: "missing" }] }),
		).toThrow(/tweaks unknown facet 'missing'/);
	});

	it("rejects an override that groups by a non-groupable facet", () => {
		expect(() =>
			resolveViewDescriptor(view(), {
				facets: [{ facet: "title", groupBy: true }],
			}),
		).toThrow(/groups by a facet whose kind is not groupable/);
	});

	it("rejects an override that sorts a non-sortable facet", () => {
		expect(() =>
			resolveViewDescriptor(view(), {
				facets: [{ facet: "status", sort: "asc" }],
			}),
		).toThrow(/sorts by a facet whose kind is not sortable/);
	});

	it("validates the base descriptor", () => {
		expect(() =>
			resolveViewDescriptor({
				id: "bad",
				type: "table",
				facets: [],
			}),
		).toThrow(/must declare at least one facet/);
	});
});
