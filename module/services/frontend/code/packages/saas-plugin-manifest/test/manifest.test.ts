import { describe, expect, it } from "vitest";

import {
	assertPluginManifest,
	definePluginManifest,
	loadPluginManifest,
	type PluginManifest,
} from "../src/index.js";

function validManifest(): PluginManifest {
	return {
		apiVersion: "plugin.codefly.dev/v1",
		kind: "Plugin",
		metadata: {
			name: "warden-guardrails",
			version: "1.4.0",
			displayName: "Warden Guardrails",
			description: "Policy guardrails for Warden.",
			publisher: "codefly.dev",
		},
		services: [{ name: "guardrails", endpoints: ["connect", "rest"] }],
		api: {
			exposes: [{ contract: "guardrails", major: 1, protocols: ["connect"] }],
			consumes: [{ contract: "accounts", major: 2, alias: "accounts" }],
		},
		events: {
			publishes: [{ type: "guardrail.triggered.v1" }],
			subscribes: [
				{ type: "identity.user.created.v1", handler: "on_user_created" },
			],
		},
		ui: {
			navigation: { label: "Guardrails", placement: "admin", priority: 40 },
			navItems: [
				{
					id: "guardrails_overview",
					label: "Guardrails",
					href: "/admin/guardrails",
					requiredPermission: "guardrail:read",
				},
			],
			routes: [{ id: "overview", path: "/admin/guardrails" }],
			widgets: [{ id: "guardrail_activity", priority: 20 }],
			services: [
				{
					alias: "guardrails",
					protocol: "rest",
					routePrefix: "/api/v1/guardrails",
					compatibility: { contract: "guardrails", major: 1 },
				},
			],
		},
		needs: [
			{ capability: "store:postgres" },
			{ capability: "cache:redis", optionality: "optional" },
		],
		permissions: [{ id: "guardrail:read" }, { id: "guardrail:write" }],
		entitlements: [{ id: "guardrails.advanced", defaultGranted: false }],
		config: [
			{ key: "GUARDRAILS_DECISION_THRESHOLD", type: "int", required: true },
			{
				key: "GUARDRAILS_SIGNING_KEY",
				type: "string",
				required: true,
				secret: true,
			},
		],
		migrations: [{ id: "0001_init", scope: "tenant" }],
		egress: [{ host: "api.openai.com", ports: [443] }, { host: "*.slack.com" }],
		lifecycle: { install: [{ job: "seed_default_policies" }] },
		integrity: {
			signature: { algorithm: "ed25519", keyId: "key-1", value: "sig" },
			artifacts: [{ path: "dist/guardrails.tar.gz", sha256: "a".repeat(64) }],
		},
	};
}

function expectRejected(mutate: (manifest: PluginManifest) => void): void {
	const manifest = validManifest();
	mutate(manifest);
	expect(() => assertPluginManifest(manifest)).toThrow(
		/Invalid plugin manifest/,
	);
}

describe("plugin manifest validation", () => {
	it("accepts a manifest that exercises every section", () => {
		expect(() => assertPluginManifest(validManifest())).not.toThrow();
	});

	it("returns the same object from loadPluginManifest", () => {
		const manifest = validManifest();
		expect(loadPluginManifest(manifest)).toBe(manifest);
	});

	it("preserves literal values through definePluginManifest", () => {
		const manifest = definePluginManifest(validManifest());
		expect(manifest.metadata.name).toBe("warden-guardrails");
		expect(manifest.permissions?.[0]?.id).toBe("guardrail:read");
	});

	it("rejects a non-object manifest", () => {
		expect(() => assertPluginManifest(null)).toThrow(/must be an object/);
		expect(() => assertPluginManifest([])).toThrow(/must be an object/);
	});

	it("rejects an unknown top-level field", () => {
		expectRejected((manifest) => {
			(manifest as unknown as Record<string, unknown>).plugins = [];
		});
	});

	it("rejects a wrong apiVersion or kind", () => {
		expectRejected((manifest) => {
			(manifest as unknown as { apiVersion: string }).apiVersion =
				"plugin.codefly.dev/v2";
		});
		expectRejected((manifest) => {
			(manifest as unknown as { kind: string }).kind = "Solution";
		});
	});

	it("rejects an invalid identity", () => {
		expectRejected((manifest) => {
			manifest.metadata.name = "Warden Guardrails";
		});
		expectRejected((manifest) => {
			manifest.metadata.version = "1.4";
		});
		expectRejected((manifest) => {
			manifest.metadata.publisher = "not a domain";
		});
	});

	it("rejects an unsupported endpoint protocol", () => {
		expectRejected((manifest) => {
			(manifest.services as unknown as { endpoints: string[] }[])[0].endpoints =
				["soap"];
		});
	});

	it("rejects a non-versioned event type", () => {
		expectRejected((manifest) => {
			(
				manifest.events as unknown as { publishes: { type: string }[] }
			).publishes[0].type = "guardrail.triggered";
		});
	});

	it("rejects a subscription without a handler", () => {
		expectRejected((manifest) => {
			delete (
				manifest.events as unknown as { subscribes: { handler?: string }[] }
			).subscribes[0].handler;
		});
	});

	it("rejects an unsafe permission id", () => {
		expectRejected((manifest) => {
			(manifest.permissions as unknown as { id: string }[])[0].id =
				"Guardrail Read";
		});
	});

	it("rejects a lower-case config key", () => {
		expectRejected((manifest) => {
			(manifest.config as unknown as { key: string }[])[0].key = "threshold";
		});
	});

	it("rejects an unsupported config type", () => {
		expectRejected((manifest) => {
			(manifest.config as unknown as { type: string }[])[0].type = "float";
		});
	});

	it("rejects a malformed migration id or scope", () => {
		expectRejected((manifest) => {
			(manifest.migrations as unknown as { id: string }[])[0].id = "init";
		});
		expectRejected((manifest) => {
			(manifest.migrations as unknown as { scope: string }[])[0].scope =
				"global";
		});
	});

	it("rejects an out-of-range egress port and bad host", () => {
		expectRejected((manifest) => {
			(manifest.egress as unknown as { ports?: number[] }[])[0].ports = [70000];
		});
		expectRejected((manifest) => {
			(manifest.egress as unknown as { host: string }[])[0].host =
				"http://api.openai.com";
		});
	});

	it("rejects a needed capability that is not namespaced", () => {
		expectRejected((manifest) => {
			(manifest.needs as unknown as { capability: string }[])[0].capability =
				"Postgres";
		});
	});

	it("rejects a short integrity artifact hash", () => {
		expectRejected((manifest) => {
			(
				manifest.integrity as unknown as { artifacts: { sha256: string }[] }
			).artifacts[0].sha256 = "abc";
		});
	});

	it("rejects duplicate ids within a section", () => {
		expectRejected((manifest) => {
			manifest.permissions = [
				{ id: "guardrail:read" },
				{ id: "guardrail:read" },
			];
		});
		expectRejected((manifest) => {
			manifest.migrations = [
				{ id: "0001_init", scope: "tenant" },
				{ id: "0001_init", scope: "shared" },
			];
		});
	});

	it("delegates ui validation to the frontend contract", () => {
		const manifest = validManifest();
		(
			manifest.ui as unknown as { navItems: { href: string }[] }
		).navItems[0].href = "admin/guardrails";
		expect(() => assertPluginManifest(manifest)).toThrow(
			/Invalid frontend plugin composition/,
		);
	});
});
