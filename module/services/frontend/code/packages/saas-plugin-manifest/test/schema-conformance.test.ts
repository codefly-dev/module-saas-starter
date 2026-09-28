import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import Ajv2020 from "ajv/dist/2020.js";
import yaml from "js-yaml";
import { describe, expect, it } from "vitest";

import { assertPluginManifest } from "../src/index.js";

const testDir = dirname(fileURLToPath(import.meta.url));
const schema = JSON.parse(
	readFileSync(join(testDir, "../plugin.codefly.schema.json"), "utf8"),
);
const example = yaml.load(
	readFileSync(join(testDir, "../examples/plugin.codefly.yaml"), "utf8"),
);

const ajv = new Ajv2020({ strict: false, allErrors: true });
const validateWithSchema = ajv.compile(schema);

function clone(): Record<string, unknown> {
	return JSON.parse(JSON.stringify(example));
}

const rec = (value: unknown): Record<string, unknown> =>
	value as Record<string, unknown>;
const arr = (value: unknown): Record<string, unknown>[] =>
	value as Record<string, unknown>[];

/**
 * Each mutation breaks exactly one field-format rule the JSON Schema owns. The
 * JSON Schema and the TypeScript validator must reject every one of them — if a
 * rule is edited in one place but not the other, the two disagree here and the
 * test fails. This is the guard that keeps the language-neutral schema and the
 * host validator from forking. UI-internal rules are excluded on purpose: the
 * schema delegates the `ui` block to the frontend contract, so it does not
 * model those rules and cannot be expected to agree on them.
 */
const OWNED_FIELD_VIOLATIONS: Record<
	string,
	(m: Record<string, unknown>) => void
> = {
	"bad semver": (m) => {
		rec(m.metadata).version = "1.4";
	},
	"bad name": (m) => {
		rec(m.metadata).name = "Acme Guardrails";
	},
	"bad publisher": (m) => {
		rec(m.metadata).publisher = "not a domain";
	},
	"unsupported protocol": (m) => {
		arr(m.services)[0].endpoints = ["soap"];
	},
	"non-positive expose major": (m) => {
		arr(rec(m.api).exposes)[0].major = 0;
	},
	"non-namespaced publish type": (m) => {
		arr(rec(m.events).publishes)[0].type = "triggered";
	},
	"non-namespaced dashboard event type": (m) => {
		arr(rec(m.dashboard).events)[0].type = "triggered";
	},
	"declared event type with two segments": (m) => {
		arr(rec(m.dashboard).events)[2].type = "guardrail.reviewed";
	},
	"declared event type in the reserved namespace": (m) => {
		arr(rec(m.dashboard).events)[2].type = "saas.review.completed";
	},
	"declared field with an unsupported kind": (m) => {
		arr(arr(rec(m.dashboard).events)[2].fields)[1].kind = "decimal";
	},
	"declared field named solution": (m) => {
		arr(arr(rec(m.dashboard).events)[2].fields)[1].name = "solution";
	},
	"declared field not snake_case": (m) => {
		arr(arr(rec(m.dashboard).events)[2].fields)[1].name = "Confidence";
	},
	"enum field without values": (m) => {
		delete arr(arr(rec(m.dashboard).events)[2].fields)[0].values;
	},
	"values on a non-enum field": (m) => {
		arr(arr(rec(m.dashboard).events)[2].fields)[1].values = ["high"];
	},
	"declared field with a non-boolean pii": (m) => {
		arr(arr(rec(m.dashboard).events)[2].fields)[3].pii = "yes";
	},
	"unknown key on a declared field": (m) => {
		arr(arr(rec(m.dashboard).events)[2].fields)[2].required = true;
	},
	"unsupported metric group_by": (m) => {
		arr(rec(m.dashboard).metrics)[0].groupBy = "region";
	},
	"unsupported metric aggregation": (m) => {
		arr(rec(m.dashboard).metrics)[0].aggregation = "median";
	},
	"count metric with a field": (m) => {
		arr(rec(m.dashboard).metrics)[0].field = "payload:amount";
	},
	"metric bucket without a time group_by": (m) => {
		arr(rec(m.dashboard).metrics)[0].groupBy = "actor";
	},
	"unsupported derived metric operation": (m) => {
		arr(rec(m.dashboard).metrics)[3].operation = "product";
	},
	"ratio metric with three inputs": (m) => {
		arr(rec(m.dashboard).metrics)[3].inputs = [
			"triggers_over_time",
			"signups_over_time",
			"triggers_by_actor",
		];
	},
	"unsupported dashboard layout": (m) => {
		arr(rec(m.dashboard).dashboards)[0].layout = "freeform";
	},
	"unsupported widget visualization": (m) => {
		arr(arr(rec(m.dashboard).dashboards)[0].widgets)[0].visualization = "pie";
	},
	"widget section on a dashboard with no sections": (m) => {
		arr(arr(rec(m.dashboard).dashboards)[0].widgets)[0].section = "overview";
	},
	"widget with no section on a dashboard with sections": (m) => {
		const dashboard = arr(rec(m.dashboard).dashboards)[0];
		dashboard.sections = [{ id: "overview", title: "Overview" }];
		for (const widget of arr(dashboard.widgets).slice(1))
			widget.section = "overview";
	},
	"section id that is not a logical id": (m) => {
		const dashboard = arr(rec(m.dashboard).dashboards)[0];
		dashboard.sections = [{ id: "Overview Band", title: "Overview" }];
		for (const widget of arr(dashboard.widgets))
			widget.section = "Overview Band";
	},
	"section with an empty title": (m) => {
		const dashboard = arr(rec(m.dashboard).dashboards)[0];
		dashboard.sections = [{ id: "overview", title: "" }];
		for (const widget of arr(dashboard.widgets)) widget.section = "overview";
	},
	"empty sections list": (m) => {
		arr(rec(m.dashboard).dashboards)[0].sections = [];
	},
	"bad subscribe handler": (m) => {
		arr(rec(m.events).subscribes)[0].handler = "Bad Handler";
	},
	"bad permission id": (m) => {
		arr(m.permissions)[0].id = "Guardrail Read";
	},
	"bad entitlement id": (m) => {
		arr(m.entitlements)[0].id = "Bad Id";
	},
	"lower-case config key": (m) => {
		arr(m.config)[0].key = "threshold";
	},
	"unsupported config type": (m) => {
		arr(m.config)[0].type = "float";
	},
	"bad migration id": (m) => {
		arr(m.migrations)[0].id = "init";
	},
	"unsupported migration scope": (m) => {
		arr(m.migrations)[0].scope = "global";
	},
	"bad egress host": (m) => {
		arr(m.egress)[0].host = "http://api.openai.com";
	},
	"out-of-range egress port": (m) => {
		arr(m.egress)[0].ports = [70000];
	},
	"non-namespaced capability": (m) => {
		arr(m.needs)[0].capability = "Postgres";
	},
	"short artifact hash": (m) => {
		arr(rec(m.integrity).artifacts)[0].sha256 = "abc";
	},
	"empty integrity": (m) => {
		m.integrity = {};
	},
	"unknown top-level field": (m) => {
		m.plugins = [];
	},
	"wrong apiVersion": (m) => {
		m.apiVersion = "plugin.codefly.dev/v2";
	},
};

describe("JSON Schema conformance", () => {
	it("accepts the reference manifest in both the schema and the validator", () => {
		expect(validateWithSchema(example)).toBe(true);
		expect(() => assertPluginManifest(example)).not.toThrow();
	});

	it("accepts a dashboard grouped into sections in both the schema and the validator", () => {
		const manifest = clone();
		const dashboard = arr(rec(manifest.dashboard).dashboards)[0];
		dashboard.sections = [
			{ id: "overview", title: "Overview", description: "The headline read." },
			{ id: "activity", title: "Activity" },
		];
		const widgets = arr(dashboard.widgets);
		widgets[0].section = "activity";
		for (const widget of widgets.slice(1)) widget.section = "overview";
		expect(validateWithSchema(manifest)).toBe(true);
		expect(() => assertPluginManifest(manifest)).not.toThrow();
	});

	it.each(Object.keys(OWNED_FIELD_VIOLATIONS))(
		"schema and validator agree on rejecting: %s",
		(name: string) => {
			const manifest = clone();
			OWNED_FIELD_VIOLATIONS[name](manifest);
			expect(validateWithSchema(manifest), "JSON Schema should reject").toBe(
				false,
			);
			expect(
				() => assertPluginManifest(manifest),
				"validator should reject",
			).toThrow();
		},
	);
});
