"use client";

import { useSyncExternalStore } from "react";

const subscribeToOrigin = () => () => {};

export function ApiDocsFrame() {
	const specUrl = useSyncExternalStore(
		subscribeToOrigin,
		() => `${window.location.origin}/api/openapi`,
		() => "",
	);

	if (!specUrl) return null;
	return (
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
	);
}
