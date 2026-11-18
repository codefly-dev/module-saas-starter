import { expect, type Page } from "@playwright/test";

export async function resolveConsentPrompt(page: Page): Promise<void> {
	const terms = page.getByRole("dialog").filter({ has: page.getByRole("heading", { name: "Review the current Terms" }) });
	const preferences = page.getByRole("dialog").filter({ has: page.getByRole("heading", { name: "Your privacy choices" }) });
	try {
		await terms.or(preferences).waitFor({ state: "visible", timeout: 3000 });
	} catch {
		return;
	}
	if (await terms.isVisible()) {
		const accept = terms.getByRole("button", { name: "Accept Terms" });
		await expect(accept).toBeEnabled();
		await accept.click();
		await expect(terms).toBeHidden();
	}

	// Returning users can already have preferences for the current policy.
	if (!(await preferences.isVisible())) return;
	await preferences.getByRole("button", { name: "Reject optional" }).click();
	await expect(preferences).toHaveCount(0);
}
