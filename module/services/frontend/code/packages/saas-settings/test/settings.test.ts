import { describe, expect, it } from "vitest";
import {
	createSettingsFieldFactory,
	createSettingsUpdate,
	defineSettingsField,
	mergeSettingsPatches,
} from "../src/index.js";

interface ExampleSettings {
	section?: {
		enabled?: boolean | null;
		label?: string | null;
		count?: number | null;
	};
}

type ExamplePatch = {
	section?: {
		enabled?: boolean;
		label?: string;
		count?: number;
	};
	tags?: string[];
};

const enabled = defineSettingsField<
	ExampleSettings,
	ExamplePatch,
	boolean,
	"section.enabled"
>(
	"section.enabled",
	true,
	(settings) => settings?.section?.enabled,
	(value) => ({ section: { enabled: value } }),
);
const label = defineSettingsField<
	ExampleSettings,
	ExamplePatch,
	string,
	"section.label"
>(
	"section.label",
	"default",
	(settings) => settings?.section?.label,
	(value) => ({ section: { label: value } }),
);
const count = defineSettingsField<
	ExampleSettings,
	ExamplePatch,
	number,
	"section.count"
>(
	"section.count",
	7,
	(settings) => settings?.section?.count,
	(value) => ({ section: { count: value } }),
);
const exampleField = createSettingsFieldFactory<
	ExampleSettings,
	ExamplePatch
>();

describe("SaaS settings fields", () => {
	it("defaults through missing, empty, and null nested values", () => {
		expect(enabled.get(undefined)).toBe(true);
		expect(enabled.get({})).toBe(true);
		expect(enabled.get({ section: {} })).toBe(true);
		expect(enabled.get({ section: { enabled: null } })).toBe(true);
		expect(enabled.has({ section: { enabled: null } })).toBe(false);
	});

	it("preserves every explicit JavaScript zero value", () => {
		const settings = {
			section: { enabled: false, label: "", count: 0 },
		};
		expect(enabled.get(settings)).toBe(false);
		expect(label.get(settings)).toBe("");
		expect(count.get(settings)).toBe(0);
		expect(enabled.lookup(settings)).toEqual({ value: false, present: true });
	});

	it("constructs typed patches and clear masks without null", () => {
		expect(enabled.patch(false)).toEqual({ section: { enabled: false } });
		expect(createSettingsUpdate(enabled.patch(false), [label.path])).toEqual({
			patch: { section: { enabled: false } },
			clearMask: { paths: ["section.label"] },
		});
	});

	it("binds a generated schema once while inferring leaf types", () => {
		const inferred = exampleField(
			"section.enabled",
			true,
			(settings) => settings?.section?.enabled,
			(value) => ({ section: { enabled: value } }),
		);
		expect(inferred.get({ section: { enabled: false } })).toBe(false);
		expect(inferred.patch(true)).toEqual({ section: { enabled: true } });
	});
});

describe("SaaS settings patch composition", () => {
	it("recursively preserves nested siblings and explicit zero values", () => {
		expect(
			mergeSettingsPatches<ExamplePatch>(
				{ section: { enabled: true, label: "before", count: 7 } },
				{ section: { enabled: false } },
				{ section: { label: "", count: 0 } },
			),
		).toEqual({
			section: { enabled: false, label: "", count: 0 },
		});
	});

	it("treats undefined as absence and arrays as replacement", () => {
		expect(
			mergeSettingsPatches<ExamplePatch>(
				{ section: { label: "kept" }, tags: ["one", "two"] },
				{ section: { label: undefined }, tags: ["three"] },
			),
		).toEqual({
			section: { label: "kept" },
			tags: ["three"],
		});
	});

	it("rejects null resets and prototype-polluting keys", () => {
		expect(() =>
			mergeSettingsPatches({
				section: { label: null },
			} as unknown as ExamplePatch),
		).toThrow(/use a clear-mask path/);

		const unsafe = JSON.parse('{"__proto__":{"admin":true}}') as ExamplePatch;
		expect(() => mergeSettingsPatches(unsafe)).toThrow(
			/unsafe settings patch key/,
		);
	});
});
