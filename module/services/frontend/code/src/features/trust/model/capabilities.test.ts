import { describe, expect, it } from "vitest";

import rawManifest from "../capability-manifest.json";
import {
	type CapabilityContext,
	type CapabilityDefinition,
	type CapabilityEvidence,
	type CapabilityManifest,
	capabilityStateAtLeast,
	effectiveCapabilityState,
	publicCapabilities,
	starterDefaultCapabilityContext,
} from "./capabilities";

const capabilityManifest = rawManifest as CapabilityManifest;

const configuredBackupCapability = capabilityManifest.capabilities.find(
	(capability) => capability.id === "operations.backup-restore",
);
if (!configuredBackupCapability) {
	throw new Error("backup capability fixture is missing");
}
const backupCapability = configuredBackupCapability;

function configuredContext(
	evidence: CapabilityEvidence[] = [],
): CapabilityContext {
	return {
		environment: "production",
		scope: "primary region",
		configuredProviders: ["backup-provider"],
		configuredSettings: ["recovery.policy"],
		evidence,
		now: new Date("2026-07-28T12:00:00Z"),
	};
}

function evidence(
	overrides: Partial<CapabilityEvidence> = {},
): CapabilityEvidence {
	return {
		id: "ev-backup-restore",
		capabilityId: backupCapability.id,
		environment: "production",
		scope: "primary region",
		owner: "operations",
		verifier: "release-manager",
		source: "private://recovery/restore-exercise",
		performedAt: "2026-07-20T10:00:00Z",
		reviewAt: "2026-08-20T10:00:00Z",
		status: "current",
		state: "operationally_verified",
		visibility: "private",
		...overrides,
	};
}

describe("capability evidence", () => {
	it("keeps unsupported starter-default claims absent", () => {
		const capabilities = publicCapabilities(
			capabilityManifest,
			starterDefaultCapabilityContext(new Date("2026-07-28T12:00:00Z")),
		);
		for (const id of [
			"privacy.export-artifact",
			"privacy.deletion-completion",
			"operations.backup-restore",
			"operations.incident-communication",
			"assurance.dpa",
			"assurance.penetration-test",
			"assurance.certification",
		]) {
			expect(
				capabilities.find((capability) => capability.id === id),
			).toMatchObject({
				state: "absent",
				label: "Adopter action required",
				summary: undefined,
			});
		}
	});

	it("does not confuse deployed configuration with verification", () => {
		expect(
			effectiveCapabilityState(backupCapability, configuredContext()),
		).toBe("configured");
	});

	it("promotes only current evidence for the exact environment and scope", () => {
		expect(
			effectiveCapabilityState(
				backupCapability,
				configuredContext([evidence()]),
			),
		).toBe("operationally_verified");
		expect(
			effectiveCapabilityState(
				backupCapability,
				configuredContext([evidence({ environment: "staging" })]),
			),
		).toBe("configured");
	});

	it("degrades claims when evidence expires or configuration is removed", () => {
		expect(
			effectiveCapabilityState(
				backupCapability,
				configuredContext([evidence({ reviewAt: "2026-07-27T10:00:00Z" })]),
			),
		).toBe("configured");
		expect(
			effectiveCapabilityState(
				backupCapability,
				configuredContext([evidence({ performedAt: "2026-07-29T10:00:00Z" })]),
			),
		).toBe("configured");

		const withoutProvider = configuredContext([evidence()]);
		withoutProvider.configuredProviders = [];
		expect(effectiveCapabilityState(backupCapability, withoutProvider)).toBe(
			"absent",
		);
	});

	it("publishes safe summaries without private evidence locations or reviewers", () => {
		const context = configuredContext([
			evidence({
				visibility: "public_summary",
				publicSummary: "Restore exercise passed for the stated scope.",
			}),
		]);
		const publicRecord = publicCapabilities(
			{
				...capabilityManifest,
				capabilities: [backupCapability],
			},
			context,
		)[0];

		expect(publicRecord.evidenceSummary).toBe(
			"Restore exercise passed for the stated scope.",
		);
		expect(publicRecord).not.toHaveProperty("source");
		expect(publicRecord).not.toHaveProperty("owner");
		expect(publicRecord).not.toHaveProperty("verifier");
	});

	it("cannot promote implemented code to attested without attestation evidence", () => {
		const capability: CapabilityDefinition = {
			...backupCapability,
			designState: "implemented",
			configuration: { providers: [], settings: [] },
		};
		expect(
			effectiveCapabilityState(capability, {
				...configuredContext([evidence()]),
				configuredProviders: [],
				configuredSettings: [],
			}),
		).toBe("operationally_verified");
	});

	it("treats external attestation as satisfying operational verification", () => {
		expect(
			capabilityStateAtLeast("externally_attested", "operationally_verified"),
		).toBe(true);
	});
});
