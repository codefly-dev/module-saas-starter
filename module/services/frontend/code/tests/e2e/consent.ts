import { expect, type Page } from "@playwright/test";

export async function resolveConsentPrompt(page: Page): Promise<void> {
	const prompt = page.getByRole("dialog", {
		name: /Terms acceptance|Optional tracking preferences/,
	});
	try {
		await prompt.waitFor({ state: "visible", timeout: 1000 });
	} catch {
		return;
	}

	const terms = page.getByRole("dialog", { name: "Terms acceptance" });
	if (await terms.isVisible()) {
		const accept = terms.getByRole("button", { name: "Accept Terms" });
		await expect(accept).toBeEnabled();
		await accept.click();
	}

	const preferences = page.getByRole("dialog", {
		name: "Optional tracking preferences",
	});
	await expect(preferences).toBeVisible();
	await preferences.getByRole("button", { name: "Reject optional" }).click();
	await expect(preferences).toHaveCount(0);
}
