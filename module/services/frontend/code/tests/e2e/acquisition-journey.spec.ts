import { signInForScenario } from "./fixture-session";
import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAs(page: Page, fixtureName: string): Promise<string> {
	await signInForScenario(page, fixtureName);
	await expect(page.getByRole("heading", { name: /^Welcome back/ })).toBeVisible({ timeout: 15000 });
	await resolveConsentPrompt(page);
	const refresh = await page.request.post("/v1/auth/refresh", { data: {} });
	expect(refresh.ok()).toBe(true);
	return ((await refresh.json()) as { accessToken: string }).accessToken;
}

async function invitationRPC(
	page: Page,
	method: string,
	token: string,
	body: Record<string, unknown>,
) {
	const response = await page.request.post(
		`/saas.accounts.v1.InvitationService/${method}`,
		{
			headers: { Authorization: `Bearer ${token}` },
			data: body,
		},
	);
	return { response, body: await response.text() };
}

test.describe("Acquisition journey", () => {
	test("legal links resolve to explicit starter content", async ({ page }) => {
		await page.goto("/legal/terms");
		await expect(
			page.getByRole("heading", { name: "Terms of Service" }),
		).toBeVisible();

		await page.goto("/legal/privacy");
		await expect(
			page.getByRole("heading", { name: "Privacy Policy" }),
		).toBeVisible();
		await expect(
			page.getByText(
				"E2E Privacy content used only to exercise configured legal routes.",
			),
		).toBeVisible();
	});

	test("public waitlist truthfully reflects the configured acquisition mode", async ({
		page,
	}) => {
		await page.goto("/waitlist?source=e2e&campaign=journey");
		await expect(
			page.getByText(
				/Registration is open|Registration is closed|Request access/,
				{ exact: true },
			),
		).toBeVisible();
	});

	test("invalid invitation credential is removed from browser-visible history", async ({
		page,
		context,
	}) => {
		const credential = "a".repeat(43);
		const inspection = page.waitForResponse(
			(response) =>
				response.url().endsWith("/invitations/accept/api") &&
				response.request().method() === "POST",
		);
		await page.goto(`/invitations/accept?token=${credential}&campaign=journey`);

		expect((await inspection).status()).toBe(404);
		await expect(page).toHaveURL(/\/invitations\/accept\?campaign=journey$/);
		await expect(
			page.getByText("Organization invitation", { exact: true }),
		).toBeVisible();
		await expect(page.locator("body")).not.toContainText(credential);
		expect(
			(await context.cookies()).find(
				(cookie) => cookie.name === "invitation_return_token",
			),
		).toMatchObject({
			httpOnly: true,
			path: "/invitations/accept",
			sameSite: "Strict",
			value: credential,
		});
	});

	test("existing user accepts an invitation and the inviter sees accepted state", async ({
		page,
		browser,
	}) => {
		const operator = await loginAs(page, "Sarah Chen");
		const slug = "invite-" + crypto.randomUUID();
		const name = "Invitation QA " + slug;
		const headers = { Authorization: `Bearer ${operator}` };
		const created = await page.request.post("/saas.accounts.v1.OrganizationService/CreateOrganization", {
			headers, data: { name, slug },
		});
		expect(created.ok(), await created.text()).toBe(true);
		const orgId: string = (await created.json()).organization.id;
		const switched = await page.request.post("/v1/auth/switch-organization", {
			headers, data: { organizationId: orgId },
		});
		expect(switched.ok(), await switched.text()).toBe(true);
		const adminToken: string = (await switched.json()).accessToken;
		const inviteeContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
		try {
			const invitation = await invitationRPC(page, "CreateInvitation", adminToken, {
				orgId, email: "dana@example.com", role: "INVITATION_ROLE_MEMBER",
			});
			expect(invitation.response.ok(), invitation.body).toBe(true);
			const invitationId: string = JSON.parse(invitation.body).invitation.id;
			const inviteePage = await inviteeContext.newPage();
			await inviteePage.goto(
				`/auth/login?next=${encodeURIComponent(`/invitations/accept?id=${invitationId}`)}`,
			);
			await expect(inviteePage.getByText("Dana Lee")).toBeVisible({
				timeout: 15000,
			});
			const authentication = inviteePage.waitForResponse(
				(response) =>
					response.url().endsWith("/v1/auth/authenticate") &&
					response.request().method() === "POST",
			);
			await inviteePage.getByText("Dana Lee").click();
			const authenticationResponse = await authentication;
			expect(
				authenticationResponse.ok(),
				await authenticationResponse.text(),
			).toBe(true);
			await expect(
				inviteePage.getByRole("button", { name: "Accept invitation" }),
			).toBeVisible({ timeout: 20000 });

			await inviteePage
				.getByRole("button", { name: "Accept invitation" })
				.click();
			await expect(inviteePage.getByText("Invitation accepted")).toBeVisible(
				{ timeout: 20000 },
			);
			await expect(inviteePage.getByText("member of " + name, { exact: false })).toBeVisible();
			await resolveConsentPrompt(inviteePage);
			const organizationSwitch = inviteePage.waitForResponse((response) => {
				const url = response.url().toLowerCase();
				return (
					url.includes("authservice/switchorganization") ||
					url.includes("/v1/auth/switch-organization")
				);
			});
			await inviteePage.getByRole("button", { name: "Continue to workspace" }).click();
			const organizationSwitchResponse = await organizationSwitch;
			expect(
				organizationSwitchResponse.ok(),
				await organizationSwitchResponse.text(),
			).toBe(true);


			const after = await invitationRPC(
				page,
				"ListInvitations",
				adminToken,
				{ orgId },
			);
			expect(after.response.ok()).toBe(true);
			const accepted = (
				JSON.parse(after.body) as {
					invitations?: Array<{ id: string; status: string }>;
				}
			).invitations?.find(
				(invitation) => invitation.id === invitationId,
			);
			expect(accepted?.status).toBe("INVITATION_STATUS_ACCEPTED");
		} finally {
			const removed = await page.request.post("/saas.accounts.v1.OrganizationService/DeleteOrganization", {
				headers: { Authorization: `Bearer ${adminToken}` }, data: { orgId, confirmSlug: slug },
			});
			expect(removed.ok(), await removed.text()).toBe(true);
			await inviteeContext.close();
		}
	});
});
