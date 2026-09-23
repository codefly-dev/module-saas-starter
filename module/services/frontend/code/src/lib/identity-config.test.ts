import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));
vi.mock("next/server", () => ({ connection: vi.fn(async () => undefined) }));

import { readIdentityConfig } from "./identity-config";

const prefix = "CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__";
const keys = [
	"IDENTITY_PROVIDER",
	"IDENTITY_AUTHORIZE_URL",
	"IDENTITY_CLIENT_ID",
	"IDENTITY_DISPLAY_NAME",
	"IDENTITY_SCOPE",
	"IDENTITY_AUTHORIZE_SELECTOR",
	"IDENTITY_CLIENT_SECRET",
];

afterEach(() => {
	for (const key of keys) {
		delete process.env[`${prefix}${key}`];
		delete process.env[
			`CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__${key}`
		];
	}
});

describe("readIdentityConfig", () => {
	it("reads the identity group from the running process", async () => {
		process.env[`${prefix}IDENTITY_PROVIDER`] = "workos";
		process.env[`${prefix}IDENTITY_AUTHORIZE_URL`] =
			"https://api.workos.com/user_management/authorize";
		process.env[`${prefix}IDENTITY_CLIENT_ID`] = "client_123";
		process.env[`${prefix}IDENTITY_DISPLAY_NAME`] = "Company login";
		process.env[`${prefix}IDENTITY_AUTHORIZE_SELECTOR`] = "authkit";

		expect(await readIdentityConfig()).toEqual({
			provider: "workos",
			authorizeURL: "https://api.workos.com/user_management/authorize",
			clientID: "client_123",
			displayName: "Company login",
			scope: undefined,
			authorizeSelector: "authkit",
		});
	});

	it("never carries a secret of the group", async () => {
		process.env.CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET =
			"must-not-leak";
		expect(JSON.stringify(await readIdentityConfig())).not.toContain(
			"must-not-leak",
		);
	});
});
