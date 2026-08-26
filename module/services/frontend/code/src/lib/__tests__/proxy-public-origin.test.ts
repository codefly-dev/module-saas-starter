import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";
import { publicRequestOrigin } from "@/proxy";

describe("publicRequestOrigin", () => {
	it("uses the ingress-forwarded proto and host over the pod's plaintext origin", () => {
		// TLS terminates at the ingress, so the pod receives http and nextUrl.origin
		// would report the wrong scheme; the browser is on https://app.example, and
		// Accounts rejects a non-loopback http public origin.
		const req = new NextRequest(
			"http://frontend.pod.internal/saas.accounts.v1.AuthService/BeginOAuth",
			{
				headers: {
					"x-forwarded-proto": "https",
					"x-forwarded-host": "app.example",
				},
			},
		);
		expect(publicRequestOrigin(req)).toBe("https://app.example");
	});

	it("takes the outermost hop when forwarded headers are comma-lists", () => {
		const req = new NextRequest("http://frontend.pod.internal/", {
			headers: {
				"x-forwarded-proto": "https, http",
				"x-forwarded-host": "app.example, internal.pod",
			},
		});
		expect(publicRequestOrigin(req)).toBe("https://app.example");
	});

	it("overrides only the scheme when x-forwarded-host is absent", () => {
		// TLS terminated at the ingress: nextUrl carries the right host but the
		// wrong (plaintext) scheme, so only the proto needs correcting.
		const req = new NextRequest("http://app.example/", {
			headers: { "x-forwarded-proto": "https" },
		});
		expect(publicRequestOrigin(req)).toBe("https://app.example");
	});

	it("falls back to nextUrl entirely without forwarded headers (local dev, direct hits)", () => {
		const req = new NextRequest(
			"http://localhost:3000/saas.accounts.v1.AuthService/BeginOAuth",
		);
		expect(publicRequestOrigin(req)).toBe("http://localhost:3000");
	});
});
