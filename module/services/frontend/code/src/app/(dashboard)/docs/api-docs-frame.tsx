"use client";

import SwaggerUI from "swagger-ui-react";
import "swagger-ui-react/swagger-ui.css";

// Self-hosted API viewer. Renders the OpenAPI spec served same-origin at
// /api/openapi with a bundled Swagger UI — no external iframe, so the CSP needs
// no third-party frame-src and the spec URL never leaves this origin (the old
// petstore.swagger.io embed leaked it via its ?url= query param).
export function ApiDocsFrame() {
	return (
		<div
			className="rounded-lg border bg-card overflow-hidden"
			style={{ height: "calc(100vh - 12rem)" }}
		>
			<SwaggerUI url="/api/openapi" />
		</div>
	);
}
