import { readFileSync } from "fs";
import { NextResponse } from "next/server";
import { resolve } from "path";

export function GET() {
	try {
		const spec = readFileSync(
			resolve(process.cwd(), "../../accounts/openapi/api.swagger.json"),
			"utf-8",
		);
		return new NextResponse(spec, {
			headers: {
				"Content-Type": "application/json",
			},
		});
	} catch {
		return NextResponse.json(
			{ error: "OpenAPI spec not found" },
			{ status: 404 },
		);
	}
}
