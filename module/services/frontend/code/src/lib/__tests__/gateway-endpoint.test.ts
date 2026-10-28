import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { gatewayBase, gatewayOrigin } from "../gateway-endpoint";

const key = "CODEFLY__ENDPOINT__FIXTURE_HOST__AUTH_GATEWAY__REST__REST";
beforeEach(() => {
	vi.stubEnv("CODEFLY__MODULE", "fixture-host");
	vi.stubEnv(key, "https://gateway.example.com/edge/");
});
afterEach(() => vi.unstubAllEnvs());

describe("gateway destination through the actual Codefly SDK", () => {
	it.each(["http://127.0.0.1:12345/local/", "https://gateway.example.com/edge/"])("preserves the delivered base path %s", (address) => {
		vi.stubEnv(key, address);
		expect(gatewayBase()).toBe(address.slice(0, -1));
		expect(gatewayOrigin()).toBe(new URL(address).origin);
	});
	it("does not pick another module or protocol", () => {
		vi.stubEnv(key, "");
		vi.stubEnv("CODEFLY__ENDPOINT__OTHER__AUTH_GATEWAY__REST__REST", "https://other.example.com");
		vi.stubEnv("CODEFLY__ENDPOINT__FIXTURE_HOST__AUTH_GATEWAY__REST__CONNECT", "https://other.example.com");
		expect(gatewayBase()).toBeNull();
	});
	it("diagnoses ambiguous composition without logging addresses or treating absence as ambiguity", () => {
		const diagnostic = vi.spyOn(console, "error").mockImplementation(() => {});
		try {
			vi.stubEnv("CODEFLY__ENDPOINT__FIXTURE_HOST__AUTH-GATEWAY__REST__REST", "https://private.example.com/edge");
			expect(gatewayBase()).toBeNull();
			expect(diagnostic).toHaveBeenCalledExactlyOnceWith(
				"gateway resolution: ambiguous auth-gateway/rest/REST endpoint in the current module",
			);
			diagnostic.mockClear();
			vi.stubEnv("CODEFLY__ENDPOINT__FIXTURE_HOST__AUTH-GATEWAY__REST__REST", undefined);
			vi.stubEnv(key, "");
			expect(gatewayBase()).toBeNull();
			expect(diagnostic).not.toHaveBeenCalled();
		} finally {
			diagnostic.mockRestore();
		}
	});
	it("refuses ambiguous delivered candidates instead of picking one", () => {
		vi.stubEnv("CODEFLY__ENDPOINT__FIXTURE_HOST__AUTH-GATEWAY__REST__REST", "https://other.example.com");
		expect(gatewayBase()).toBeNull();
	});
	it.each(["", "not a URL", "file:///tmp/socket", "ftp://gateway.example.com", "https://user:password@gateway.example.com", "https://gateway.example.com/?target=elsewhere", "https://gateway.example.com/#fragment"])("refuses unsafe destination %s", (address) => {
		vi.stubEnv(key, address);
		expect(gatewayBase()).toBeNull();
		expect(gatewayOrigin()).toBeNull();
	});
});
