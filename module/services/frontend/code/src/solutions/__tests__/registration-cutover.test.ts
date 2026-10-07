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
			"ListSolutionHostBindings", "ListSolutionRegistrations",
		]);
		for (const name of ["SolutionFrontendBinding", "SolutionBackendBinding"]) {
			const message = registry.messages.find((message) => message.name === name);
			expect(message).toBeDefined();
			expect(message!.fields.map((field) => field.name)).not.toContain("lease_expires_at");
		}
		for (const name of ["PutSolutionRegistrationRequest", "DeleteSolutionRegistrationRequest"]) {
			expect(registry.messages.map((message) => message.name)).not.toContain(name);
		}
	});
});
