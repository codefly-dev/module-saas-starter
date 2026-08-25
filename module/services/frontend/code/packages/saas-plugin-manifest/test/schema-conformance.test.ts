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
		rec(m.metadata).name = "Warden Guardrails";
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
	"unversioned publish type": (m) => {
		arr(rec(m.events).publishes)[0].type = "guardrail.triggered";
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
