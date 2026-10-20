import { cookies } from "next/headers";
import { NextResponse } from "next/server";

import { gatewayBase } from "@/lib/gateway-endpoint";

const COOKIE = "waitlist_verification_token";

export async function POST(request: Request) {
	const cookieStore = await cookies();
	const token = cookieStore.get(COOKIE)?.value;
	if (!token) {
		return NextResponse.json(
			{ message: "This verification link is invalid or no longer available." },
			{ status: 400 },
		);
	}
	const gateway = gatewayBase();
	if (!gateway) {
		console.error("waitlist route: the composition states no API gateway endpoint");
		return NextResponse.json(
			{ message: "The waitlist service is not reachable from this deployment." },
			{ status: 503 },
		);
	}
	const upstream = await fetch(`${gateway}/v1/waitlist:verify`, {
		method: "POST",
		cache: "no-store",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ token }),
	});
	const body = await upstream.json().catch(() => ({}));
	const response = NextResponse.json(body, { status: upstream.status });
	response.headers.set("Cache-Control", "no-store");
	response.headers.set("Referrer-Policy", "no-referrer");
	if (
		upstream.ok ||
		(upstream.status >= 400 &&
			upstream.status < 500 &&
			upstream.status !== 429)
	) {
		response.cookies.set(COOKIE, "", {
			httpOnly: true,
			secure: process.env.NODE_ENV === "production",
			sameSite: "strict",
			path: "/waitlist/verify",
			maxAge: 0,
		});
	}
	return response;
}
