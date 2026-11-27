import { expect, test } from "@playwright/test";
import { signInForScenario } from "./fixture-session";

for (const width of [400, 1280]) {
	test(`the notification count fits its header button at ${width}px`, async ({
		page,
	}) => {
		await page.setViewportSize({ width, height: 800 });
		let unreadCount = 1;
		await page.route("**/api/notifications/stream", (route) =>
			route.fulfill({
				contentType: "text/event-stream",
				body: `data: ${JSON.stringify({ unreadCount })}\n\n`,
			}),
		);
		await signInForScenario(page, "Carol Davis");
		for (const count of [1, 9, 10, 99, 100, 0]) {
			unreadCount = count;
			await page.goto("/");
			const name = count ? `Notifications, ${count} unread` : "Notifications";
			const button = page.getByRole("button", { name, exact: true });
			await expect(button).toBeVisible();
			await expect(button).toHaveText(
				count > 99 ? "99+" : count ? String(count) : "",
			);
			expect(
				await button.evaluate(
					(element) => element.scrollWidth - element.clientWidth,
				),
			).toBeLessThanOrEqual(0);
		}
	});
}
