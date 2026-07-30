import { describe, expect, it } from "vitest";
import { createBrowserOnboardingDraftStore } from "./browser-draft-store";

class MemoryStorage {
	private readonly values = new Map<string, string>();
	removed: string[] = [];

	getItem(key: string): string | null {
		return this.values.get(key) ?? null;
	}

	setItem(key: string, value: string): void {
		this.values.set(key, value);
	}

	removeItem(key: string): void {
		this.removed.push(key);
		this.values.delete(key);
	}
}

describe("createBrowserOnboardingDraftStore", () => {
	it("round-trips and clears an organization draft", () => {
		const storage = new MemoryStorage();
		const store = createBrowserOnboardingDraftStore(storage);

		expect(store.load()).toBeNull();
		store.save({ name: "Acme", slug: "acme" });
		expect(store.load()).toEqual({ name: "Acme", slug: "acme" });
		store.clear();
		expect(store.load()).toBeNull();
	});

	it("sanitizes partial values instead of trusting browser storage", () => {
		const storage = new MemoryStorage();
		storage.setItem(
			"saas-starter:onboarding-organization-draft",
			JSON.stringify({ name: 42, slug: "saved-slug", ignored: true }),
		);

		expect(createBrowserOnboardingDraftStore(storage).load()).toEqual({
			name: "",
			slug: "saved-slug",
		});
	});

	it("removes malformed JSON and recovers with an empty draft", () => {
		const storage = new MemoryStorage();
		storage.setItem("saas-starter:onboarding-organization-draft", "{not-json");
		const store = createBrowserOnboardingDraftStore(storage);

		expect(store.load()).toBeNull();
		expect(storage.removed).toEqual([
			"saas-starter:onboarding-organization-draft",
		]);
		expect(store.load()).toBeNull();
	});
});
