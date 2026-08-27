import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const testDir = dirname(fileURLToPath(import.meta.url));
const schema = JSON.parse(
	readFileSync(join(testDir, "../plugin.codefly.schema.json"), "utf8"),
) as {
	required: string[];
	properties: Record<string, unknown>;
	$defs: { protocol: { enum: string[] } };
};

const MANIFEST_SECTIONS = [
	"apiVersion",
	"kind",
	"metadata",
	"services",
	"api",
	"events",
	"ui",
	"dashboard",
	"needs",
	"permissions",
	"entitlements",
	"config",
	"migrations",
	"egress",
	"lifecycle",
	"integrity",
];

describe("canonical JSON Schema", () => {
	it("declares exactly the manifest sections", () => {
		expect(Object.keys(schema.properties).sort()).toEqual(
			[...MANIFEST_SECTIONS].sort(),
		);
	});

	it("requires the header and identity only", () => {
		expect(schema.required.sort()).toEqual(["apiVersion", "kind", "metadata"]);
	});

	it("pins the same protocol set as the validator", () => {
		expect(schema.$defs.protocol.enum).toEqual(["connect", "rest", "grpc"]);
	});
});
