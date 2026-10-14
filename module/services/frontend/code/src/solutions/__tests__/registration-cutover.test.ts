import { describe, expect, it } from "vitest";
import { file_saas_accounts_v1_solution_registry as hostRegistry } from "@/gen/saas/accounts/v1/solution_registry_pb";
import { SolutionRegistryService as hostService } from "@/gen/saas/accounts/v1/solution_registry_service_pb";
import { file_saas_accounts_v1_solution_registry as sdkRegistry } from "../../../packages/saas-sdk/generated/typescript/src/gen/saas/accounts/v1/solution_registry_pb";
import { SolutionRegistryService as sdkService } from "../../../packages/saas-sdk/generated/typescript/src/gen/saas/accounts/v1/solution_registry_service_pb";

describe.each([
	["host", hostRegistry, hostService],
	["SDK", sdkRegistry, sdkService],
] as const)("%s registry wire contract", (_name, registry, service) => {
	it("exposes only reads and no runtime registration messages or lease fields", () => {
		expect(service.methods.map((method) => method.name)).toEqual([
			"ListSolutionHostBindings", "ListSolutionRegistrations", "GetRetainedSolutionFrontend",
		]);
		const frontend = registry.messages.find((message) => message.name === "SolutionFrontendBinding");
		expect(frontend).toBeDefined();
		expect(frontend!.fields.map((field) => field.name)).not.toContain("lease_expires_at");
		// Nothing a running process reports is on the wire (#1118): the reported
		// backend half is deleted as a message, the record carries no `backend`,
		// and the version two reported halves were compared on is gone with them.
		expect(frontend!.fields.map((field) => field.name)).not.toContain("contract_version");
		const record = registry.messages.find((message) => message.name === "SolutionRegistration");
		expect(record).toBeDefined();
		expect(record!.fields.map((field) => field.name)).not.toContain("backend");
		for (const name of ["PutSolutionRegistrationRequest", "DeleteSolutionRegistrationRequest", "SolutionBackendBinding"]) {
			expect(registry.messages.map((message) => message.name)).not.toContain(name);
		}
		const status = registry.enums.find((candidate) => candidate.name === "SolutionRegistrationStatus");
		expect(status).toBeDefined();
		expect(status!.values.map((value) => value.name)).not.toContain("SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE");
	});
});
