import { describe, expect, it, vi } from "vitest";

import { createBrowserOnboardingReminderStore } from "./browser-reminder-store";

function fakeStorage(seed?: string) {
	const values = new Map<string, string>();
	if (seed !== undefined)
		values.set("saas-starter:onboarding-reminder-dismissed", seed);
	return {
		values,
		getItem: (key: string) => values.get(key) ?? null,
		setItem: (key: string, value: string) => {
			values.set(key, value);
		},
	};
}

describe("the workspace-setup reminder remembers a dismissal", () => {
	it("shows the reminder until it is dismissed", () => {
		const store = createBrowserOnboardingReminderStore(fakeStorage());
		expect(store.isDismissed("org-1")).toBe(false);
		store.dismiss("org-1");
		expect(store.isDismissed("org-1")).toBe(true);
	});

	it("survives a reload", () => {
		const storage = fakeStorage();
		createBrowserOnboardingReminderStore(storage).dismiss("org-1");
		expect(
			createBrowserOnboardingReminderStore(storage).isDismissed("org-1"),
		).toBe(true);
	});

	// A viewer in two workspaces has finished setup in neither by dismissing the
	// nudge in one; a single flag would hide it where it is still needed.
	it("keeps the dismissal to the organization it was made in", () => {
		const store = createBrowserOnboardingReminderStore(fakeStorage());
		store.dismiss("org-1");
		expect(store.isDismissed("org-2")).toBe(false);
	});

	it("notifies subscribers once per new dismissal", () => {
		const store = createBrowserOnboardingReminderStore(fakeStorage());
		const listener = vi.fn();
		const unsubscribe = store.subscribe(listener);
		store.dismiss("org-1");
		expect(listener).toHaveBeenCalledTimes(1);
		store.dismiss("org-1");
		expect(listener).toHaveBeenCalledTimes(1);
		unsubscribe();
		store.dismiss("org-2");
		expect(listener).toHaveBeenCalledTimes(1);
	});

	// `useSyncExternalStore` reads the snapshot on every render, so the store must
	// not re-parse storage each time.
	it("reads storage once rather than on every check", () => {
		const storage = fakeStorage('["org-1"]');
		const getItem = vi.spyOn(storage, "getItem");
		const store = createBrowserOnboardingReminderStore(storage);
		store.isDismissed("org-1");
		store.isDismissed("org-1");
		store.isDismissed("org-2");
		expect(getItem).toHaveBeenCalledTimes(1);
	});

	describe("degrades toward showing the reminder, never toward hiding it", () => {
		it("has no storage at all (server render, blocked site data)", () => {
			const store = createBrowserOnboardingReminderStore(null);
			expect(store.isDismissed("org-1")).toBe(false);
			expect(() => store.dismiss("org-1")).not.toThrow();
			// The dismissal still holds for this page; it just cannot outlive it.
			expect(store.isDismissed("org-1")).toBe(true);
		});

		it("cannot read storage", () => {
			const store = createBrowserOnboardingReminderStore({
				getItem: () => {
					throw new Error("blocked");
				},
				setItem: () => {},
			});
			expect(store.isDismissed("org-1")).toBe(false);
		});

		it("cannot write storage", () => {
			const store = createBrowserOnboardingReminderStore({
				getItem: () => null,
				setItem: () => {
					throw new Error("quota");
				},
			});
			expect(() => store.dismiss("org-1")).not.toThrow();
			expect(store.isDismissed("org-1")).toBe(true);
		});

		it.each(["not json", '{"org-1":true}', "[1,2,3]", '["", null]'])(
			"finds %s stored",
			(seed) => {
				const store = createBrowserOnboardingReminderStore(fakeStorage(seed));
				expect(store.isDismissed("org-1")).toBe(false);
			},
		);
	});

	it("ignores an empty organization id rather than storing one", () => {
		const storage = fakeStorage();
		const store = createBrowserOnboardingReminderStore(storage);
		store.dismiss("");
		expect(store.isDismissed("")).toBe(false);
		expect(storage.values.size).toBe(0);
	});
});
