import { describe, expect, it } from "vitest";

import {
	defineFrontendPluginCapabilities,
	FRONTEND_PLUGIN_CAPABILITIES_CONNECT_PROCEDURE,
	FRONTEND_PLUGIN_CAPABILITIES_REST_PATH,
	frontendPluginCapabilitiesToJson,
	parseFrontendPluginCapabilities,
	supportsFrontendPluginContract,
} from "../src/capabilities.js";

describe("frontend plugin backend capability contract", () => {
	it("builds deterministic ProtoJSON from the generated schema", () => {
		const capabilities = defineFrontendPluginCapabilities({
			contract: "example.api",
			contractMajor: 1,
			capabilities: ["traffic.read", "calls.read"],
		});

		expect(frontendPluginCapabilitiesToJson(capabilities)).toEqual({
			schemaVersion: 1,
			contract: "example.api",
			contractMajor: 1,
			capabilities: ["calls.read", "traffic.read"],
		});
		expect(Object.isFrozen(capabilities)).toBe(true);
		expect(Object.isFrozen(capabilities.capabilities)).toBe(true);
	});

	it("parses strict ProtoJSON and checks the installed contract", () => {
		const capabilities = parseFrontendPluginCapabilities({
			schemaVersion: 1,
			contract: "example.api",
			contractMajor: 2,
			capabilities: ["traffic.read"],
		});

		expect(
			supportsFrontendPluginContract(capabilities, {
				contract: "example.api",
				major: 2,
			}),
		).toBe(true);
		expect(
			supportsFrontendPluginContract(capabilities, {
				contract: "example.api",
				major: 1,
			}),
		).toBe(false);
	});

	it.each([
		[{ contract: "example.api", contractMajor: 0 }, /positive integer/],
		[{ contract: "unsafe contract", contractMajor: 1 }, /unsafe/],
		[
			{
				contract: "example.api",
				contractMajor: 1,
				capabilities: ["traffic.read", "traffic.read"],
			},
			/unique/,
		],
	] as const)(
		"rejects invalid authored capability input %#",
		(input, error) => {
			expect(() => defineFrontendPluginCapabilities(input)).toThrow(error);
		},
	);

	it("rejects unknown ProtoJSON fields and unsupported schema versions", () => {
		expect(() =>
			parseFrontendPluginCapabilities({
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
				privateEndpoint: "http://backend.internal",
			}),
		).toThrow();
		expect(() =>
			parseFrontendPluginCapabilities({
				schemaVersion: 2,
				contract: "example.api",
				contractMajor: 1,
			}),
		).toThrow("unsupported schema version");
	});

	it("publishes fixed REST and Connect operations", () => {
		expect(FRONTEND_PLUGIN_CAPABILITIES_REST_PATH).toBe(
			"/.well-known/codefly/frontend-plugin-capabilities",
		);
		expect(FRONTEND_PLUGIN_CAPABILITIES_CONNECT_PROCEDURE).toBe(
			"/saas.frontend.plugin.v1.FrontendPluginCapabilityService/GetFrontendPluginCapabilities",
		);
	});
});
