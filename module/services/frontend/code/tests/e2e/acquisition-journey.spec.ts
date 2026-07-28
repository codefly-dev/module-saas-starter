import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAs(page: Page, fixtureName: string): Promise<string> {
	await page.goto("/auth/login");
	await expect(page.getByText(fixtureName)).toBeVisible({ timeout: 15000 });
	await page.getByText(fixtureName).click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 15000 });
	await resolveConsentPrompt(page);
	const refresh = await page.request.post("/v1/auth/refresh", { data: {} });
	expect(refresh.ok()).toBe(true);
	return ((await refresh.json()) as { accessToken: string }).accessToken;
}

function claims(token: string): { org: string; sub: string } {
	const [, payload] = token.split(".");
	if (!payload) throw new Error("access token has no payload");
	return JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as {
		org: string;
		sub: string;
	};
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
		const adminToken = await loginAs(page, "Sarah Chen");
		const orgId = claims(adminToken).org;
		const listed = await invitationRPC(page, "ListInvitations", adminToken, {
			orgId,
		});
		expect(listed.response.ok()).toBe(true);
		const existing = (
			JSON.parse(listed.body) as {
				invitations?: Array<{
					id: string;
					email: string;
					status: string;
				}>;
			}
		).invitations?.find(
			(invitation) =>
				invitation.email === "dana@example.com" &&
				invitation.status === "INVITATION_STATUS_PENDING",
		);

		let invitationId = existing?.id;
		if (!invitationId) {
			const created = await invitationRPC(
				page,
				"CreateInvitation",
				adminToken,
				{
					orgId,
					email: "dana@example.com",
					role: "INVITATION_ROLE_MEMBER",
				},
			);
			expect(created.response.ok(), created.body).toBe(true);
			invitationId = (
				JSON.parse(created.body) as { invitation?: { id?: string } }
			).invitation?.id;
		}
		expect(invitationId).toBeTruthy();

		const inviteeContext = await browser.newContext({
			baseURL: new URL(page.url()).origin,
		});
		const inviteePage = await inviteeContext.newPage();
		let inviteeUserId = "";
		try {
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
			inviteeUserId = claims(
				(
					(await authenticationResponse.json()) as {
						accessToken: string;
					}
				).accessToken,
			).sub;
			await expect(
				inviteePage.getByRole("button", { name: "Accept invitation" }),
			).toBeVisible({ timeout: 20000 });

			const organizationSwitch = inviteePage.waitForResponse((response) => {
				const url = response.url().toLowerCase();
				return (
					url.includes("authservice/switchorganization") ||
					url.includes("/v1/auth/switch-organization")
				);
			});
			await inviteePage
				.getByRole("button", { name: "Accept invitation" })
				.click();
			const organizationSwitchResponse = await organizationSwitch;
			expect(
				organizationSwitchResponse.ok(),
				await organizationSwitchResponse.text(),
			).toBe(true);
			await expect(inviteePage.getByText("Invitation accepted")).toBeVisible(
				{ timeout: 20000 },
			);
			await expect(inviteePage.getByText(/member of Acme Corp/)).toBeVisible();

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
			if (inviteeUserId) {
				const cleanup = await page.request.post(
					"/saas.accounts.v1.OrganizationService/RemoveMember",
					{
						headers: { Authorization: `Bearer ${adminToken}` },
						data: { orgId, userId: inviteeUserId },
					},
				);
				expect(cleanup.ok(), await cleanup.text()).toBe(true);
			}
			await inviteeContext.close();
		}
	});
});
