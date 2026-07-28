import { cookies } from "next/headers";
import { NextResponse } from "next/server";

const INVITATION_COOKIE = "invitation_return_token";
const UUID =
	/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

export async function POST(request: Request) {
	const body = (await request.json().catch(() => null)) as {
		action?: string;
		invitationId?: string;
	} | null;
	if (!body || (body.action !== "inspect" && body.action !== "accept")) {
		return NextResponse.json({ error: "Invalid request" }, { status: 400 });
	}

	const cookieStore = await cookies();
	const token = cookieStore.get(INVITATION_COOKIE)?.value;
	const invitationId =
		body.invitationId && UUID.test(body.invitationId)
			? body.invitationId
			: undefined;
	if (!token && !invitationId) {
		return NextResponse.json(
			{ error: "This invitation link is invalid or no longer available." },
			{ status: 400 },
		);
	}
	if (body.action === "inspect" && !token) {
		return NextResponse.json({ actionable: true });
	}

	const upstreamPath =
		body.action === "inspect"
			? "/v1/invitations:inspect"
			: "/v1/invitations:accept";
	const authorization = request.headers.get("authorization");
	const upstream = await fetch(new URL(upstreamPath, request.url), {
		method: "POST",
		cache: "no-store",
		headers: {
			"Content-Type": "application/json",
			...(authorization ? { Authorization: authorization } : {}),
		},
		body: JSON.stringify(
			body.action === "inspect"
				? { token }
				: token
					? { token }
					: { invitationId },
		),
	});
	const responseBody = await upstream.json().catch(() => ({}));
	const response = NextResponse.json(responseBody, { status: upstream.status });
	response.headers.set("Cache-Control", "no-store");
	response.headers.set("Referrer-Policy", "no-referrer");
	if (body.action === "accept" && upstream.ok) {
		response.cookies.set(INVITATION_COOKIE, "", {
			httpOnly: true,
			secure: process.env.NODE_ENV === "production",
			sameSite: "strict",
			path: "/invitations/accept",
			maxAge: 0,
		});
	}
	return response;
}
