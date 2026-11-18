import { expect, type Page } from "@playwright/test";
import type { FixtureUser } from "../../src/lib/fixtures/types";

export async function signInForScenario(page: Page, name: string) {
	await page.goto("/auth/login");
	if (name !== "Sarah Chen") {
		await page.getByRole("button", { name: new RegExp(name) }).click();
		return;
	}

	// Platform administration uses a seeded operator outside the page-role picker.
	const fixture = await page.request.get("/api/fixtures");
	await expect(fixture).toBeOK();
	const { users }: { users: FixtureUser[] } = await fixture.json();
	const operator = users.find((user) => user.name === name);
	if (!operator) throw new Error("The active fixture has no platform operator");
	const status = await page.evaluate(async (user) => {
		const response = await fetch("/v1/auth/authenticate", {
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({
				provider: user.provider,
				fixture: { token: user.fixture_token ?? user.provider_id },
				device_info: navigator.userAgent.slice(0, 512),
			}),
		});
		return response.status;
	}, operator);
	expect(status).toBe(200);
	await page.goto("/");
}
