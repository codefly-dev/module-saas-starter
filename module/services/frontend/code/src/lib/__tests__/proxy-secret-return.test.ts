import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";
import { proxy } from "@/proxy";

describe("secret return handling", () => {
	it("captures an invitation token in an HttpOnly cookie and redacts the URL", () => {
		const token = "a".repeat(43);
		const response = proxy(
			new NextRequest(
				`https://app.example/invitations/accept?token=${token}&campaign=launch`,
			),
		);

		expect(response.status).toBe(307);
		expect(response.headers.get("location")).toBe(
			"https://app.example/invitations/accept?campaign=launch",
		);
		const cookie = response.headers.get("set-cookie") ?? "";
		expect(cookie).toContain("invitation_return_token=");
		expect(cookie).toContain("HttpOnly");
		expect(cookie).toContain("SameSite=strict");
		expect(response.headers.get("referrer-policy")).toBe("no-referrer");
	});

	it("does not persist a malformed token", () => {
		const response = proxy(
			new NextRequest("https://app.example/waitlist/verify?token=too-short"),
		);
		expect(response.headers.get("location")).toBe(
			"https://app.example/waitlist/verify",
		);
		expect(response.headers.get("set-cookie")).toBeNull();
	});

	it.each(["/invitations/accept/api", "/waitlist/verify/api"])(
		"keeps the secret bridge public: %s",
		(path) => {
			const response = proxy(new NextRequest(`https://app.example${path}`));
			expect(response.status).toBe(200);
			expect(response.headers.get("location")).toBeNull();
		},
	);
});
