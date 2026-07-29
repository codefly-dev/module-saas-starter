import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";

import { loadPublicCapabilities } from "./capabilities.server";

vi.mock("server-only", () => ({}));

const contextPathEnv = "TRUST_CAPABILITY_CONTEXT_FILE";

afterEach(() => {
	vi.unstubAllEnvs();
});

describe("server capability context", () => {
	it("re-evaluates evidence freshness for every projection", () => {
		const root = mkdtempSync(join(tmpdir(), "capability-context-"));
		const path = join(root, "production.json");
		writeFileSync(
			path,
			JSON.stringify({
				schemaVersion: 1,
				environment: "production",
				scope: "primary region",
				configuredProviders: ["backup-provider"],
				configuredSettings: ["recovery.policy"],
				evidence: [
					{
						id: "ev-backup",
						capabilityId: "operations.backup-restore",
						environment: "production",
						scope: "primary region",
						owner: "operations",
						verifier: "release-manager",
						source: "private://recovery/restore-exercise",
						performedAt: "2026-07-20T10:00:00Z",
						reviewAt: "2026-07-29T10:00:00Z",
						status: "current",
						state: "operationally_verified",
						visibility: "private",
					},
				],
			}),
		);
		vi.stubEnv(contextPathEnv, path);

		try {
			expect(
				loadPublicCapabilities(new Date("2026-07-28T12:00:00Z")).find(
					(capability) => capability.id === "operations.backup-restore",
				)?.state,
			).toBe("operationally_verified");
			expect(
				loadPublicCapabilities(new Date("2026-07-30T12:00:00Z")).find(
					(capability) => capability.id === "operations.backup-restore",
				)?.state,
			).toBe("configured");
		} finally {
			rmSync(root, { recursive: true, force: true });
		}
	});

	it("rejects evidence for an undeclared capability", async () => {
		const { parseCapabilityContext } = await import("./capabilities.server");
		expect(() =>
			parseCapabilityContext(
				{
					schemaVersion: 1,
					environment: "production",
					scope: "primary region",
					configuredProviders: [],
					configuredSettings: [],
					evidence: [
						{
							id: "ev-unknown",
							capabilityId: "unknown.capability",
							environment: "production",
							scope: "primary region",
							owner: "operations",
							verifier: "release-manager",
							source: "private://evidence",
							performedAt: "2026-07-20T10:00:00Z",
							reviewAt: "2026-08-20T10:00:00Z",
							status: "current",
							state: "operationally_verified",
							visibility: "private",
						},
					],
				},
				{ schemaVersion: 1, capabilities: [] },
			),
		).toThrow(/does not reference a declared capability/);
	});
});
