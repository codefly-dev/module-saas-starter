import { readFileSync } from "fs";
import { NextResponse } from "next/server";
import { resolve } from "path";

export function GET() {
	try {
		const spec = readFileSync(
			resolve(process.cwd(), "../../api/openapi/user.swagger.json"),
			"utf-8",
		);
		return new NextResponse(spec, {
			headers: {
				"Content-Type": "application/json",
				"Access-Control-Allow-Origin": "*",
			},
		});
	} catch {
		return NextResponse.json(
			{ error: "OpenAPI spec not found" },
			{ status: 404 },
		);
	}
}
