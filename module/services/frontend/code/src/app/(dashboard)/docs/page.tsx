"use client";

import { useSyncExternalStore } from "react";

const subscribeToOrigin = () => () => {};

export default function DocsPage() {
	const specUrl = useSyncExternalStore(
		subscribeToOrigin,
		() => `${window.location.origin}/api/openapi`,
		() => "",
	);

	if (!specUrl) return null;

	return (
		<div className="space-y-4">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">API Documentation</h1>
				<p className="text-muted-foreground">
					Interactive API reference powered by Swagger UI.
				</p>
			</div>
			<div
				className="rounded-lg border bg-card overflow-hidden"
				style={{ height: "calc(100vh - 12rem)" }}
			>
				<iframe
					src={`https://petstore.swagger.io/?url=${encodeURIComponent(specUrl)}`}
					className="w-full h-full border-0"
					title="API Documentation"
				/>
			</div>
		</div>
	);
}
