import type { OnboardingDraftStore, OrganizationDraft } from "./controller";

const DRAFT_KEY = "saas-starter:onboarding-organization-draft";

export function createBrowserOnboardingDraftStore(
	storage: Pick<Storage, "getItem" | "setItem" | "removeItem">,
): OnboardingDraftStore {
	return {
		load(): OrganizationDraft | null {
			const value = storage.getItem(DRAFT_KEY);
			if (!value) return null;
			try {
				const parsed = JSON.parse(value) as Partial<OrganizationDraft>;
				return {
					name: typeof parsed.name === "string" ? parsed.name : "",
					slug: typeof parsed.slug === "string" ? parsed.slug : "",
				};
			} catch {
				storage.removeItem(DRAFT_KEY);
				return null;
			}
		},
		save(draft): void {
			storage.setItem(DRAFT_KEY, JSON.stringify(draft));
		},
		clear(): void {
			storage.removeItem(DRAFT_KEY);
		},
	};
}
