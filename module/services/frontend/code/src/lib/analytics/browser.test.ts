import { describe, expect, it, vi } from "vitest";
import { captureAttribution } from "./attribution";
import {
	type AnalyticsStorage,
	BrowserAnalytics,
	type BrowserAnalyticsSink,
	type BrowserCapture,
	getOrCreateAnonymousID,
} from "./browser";

class MemoryStorage implements AnalyticsStorage {
	readonly values = new Map<string, string>();
	getItem(key: string) {
		return this.values.get(key) ?? null;
	}
	setItem(key: string, value: string) {
		this.values.set(key, value);
	}
	removeItem(key: string) {
		this.values.delete(key);
	}
}

class MemorySink implements BrowserAnalyticsSink {
	readonly events: BrowserCapture[] = [];
	readonly aliases: [string, string][] = [];
	readonly identities: [string, string | undefined][] = [];
	readonly groups: string[] = [];
	resetCount = 0;
	async capture(event: BrowserCapture) {
		this.events.push(event);
	}
	async identify(userId: string, organizationId?: string) {
		this.identities.push([userId, organizationId]);
	}
	async alias(anonymousId: string, userId: string) {
		this.aliases.push([anonymousId, userId]);
	}
	async group(organizationId: string) {
		this.groups.push(organizationId);
	}
	reset() {
		this.resetCount++;
	}
}

describe("browser analytics", () => {
	it("does not collect optional events before consent or after withdrawal", async () => {
		const sink = new MemorySink();
		const analytics = new BrowserAnalytics({
			sink,
			storage: new MemoryStorage(),
		});
		expect(await analytics.track("signup_started")).toBe(false);
		analytics.setConsent("product", "granted");
		expect(await analytics.track("signup_started", { provider: "oidc" })).toBe(
			true,
		);
		analytics.setConsent("product", "withdrawn");
		expect(await analytics.track("signup_started")).toBe(false);
		expect(sink.events).toHaveLength(1);
		expect(sink.resetCount).toBe(1);
	});

	it("aliases once and rotates identity between users on shared devices", async () => {
		const storage = new MemoryStorage();
		const sink = new MemorySink();
		const analytics = new BrowserAnalytics({
			sink,
			storage,
			consent: { product: "granted" },
		});
		await analytics.identify("user-1", "org-1");
		await analytics.identify("user-1", "org-1");
		await analytics.identify("user-2", "org-2");
		expect(sink.aliases).toHaveLength(2);
		expect(sink.aliases[0]?.[0]).not.toBe(sink.aliases[1]?.[0]);
		expect(sink.groups).toEqual(["org-1", "org-1", "org-2"]);
	});

	it("generates a durable anonymous ID without reading or writing a URL", () => {
		const storage = new MemoryStorage();
		const first = getOrCreateAnonymousID(storage);
		expect(getOrCreateAnonymousID(storage)).toBe(first);
		expect([...storage.values.values()].join(" ")).not.toContain("http");
	});

	it("preserves first and last touch separately and stores only the referrer host", () => {
		const storage = new MemoryStorage();
		const first = captureAttribution(
			storage,
			new URL("https://app.example/?utm_source=partner&utm_campaign=launch"),
			"https://search.example/private/path?q=secret",
		);
		const second = captureAttribution(
			storage,
			new URL("https://app.example/?utm_source=newsletter"),
		);
		expect(first.firstTouchCampaign).toBe("launch");
		expect(second.firstTouchSource).toBe("partner");
		expect(second.lastTouchSource).toBe("newsletter");
		expect(JSON.stringify([...storage.values])).not.toContain("private/path");
	});

	it("rejects properties outside the registry before transport", async () => {
		vi.stubGlobal("crypto", { randomUUID: () => "event-id" });
		const analytics = new BrowserAnalytics({
			sink: new MemorySink(),
			storage: new MemoryStorage(),
			consent: { product: "granted" },
		});
		await expect(
			analytics.track("signup_started", { email: "private@example.com" }),
		).rejects.toThrow("not registered");
		await expect(
			analytics.track("signup_started", { provider: "private@example.com" }),
		).rejects.toThrow("forbidden data");
		await expect(
			analytics.track("signup_started", { provider: 42 }),
		).rejects.toThrow("must be a string");
		vi.unstubAllGlobals();
	});

	it("rejects query strings and sensitive values in context", () => {
		const input = {
			sink: new MemorySink(),
			storage: new MemoryStorage(),
			consent: { product: "granted" as const },
		};
		expect(
			() =>
				new BrowserAnalytics({
					...input,
					context: { route: "/onboarding?invite_token=secret" },
				}),
		).toThrow("must not contain a query");
		expect(
			() =>
				new BrowserAnalytics({
					...input,
					context: {
						featureFlags: {
							checkout: "Bearer abcdefghijklmnopqrstuvwxyz",
						},
					},
				}),
		).toThrow("contains forbidden data");
	});
});
