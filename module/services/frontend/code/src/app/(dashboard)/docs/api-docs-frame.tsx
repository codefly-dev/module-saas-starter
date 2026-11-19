"use client";

import { useQuery } from "@tanstack/react-query";
import SwaggerUI from "swagger-ui-react";
import "swagger-ui-react/swagger-ui.css";
import { Button, EmptyState, ErrorState, Panel, Spinner } from "@/shared/ui";

class DocumentationFailure extends Error {
	constructor(readonly status: number) {
		super("Documentation unavailable");
	}
}

// Keep the specification on this origin. Swagger remains the specialized API
// renderer; the host owns readable loading, failure and unavailable states.
export function ApiDocsFrame() {
	const query = useQuery({
		queryKey: ["api-documentation"],
		retry: false,
		queryFn: async ({ signal }) => {
			const response = await fetch("/api/openapi", { signal });
			if (!response.ok) throw new DocumentationFailure(response.status);
			const spec = await response.json();
			if (!spec || typeof spec !== "object" || !(spec.openapi || spec.swagger))
				throw new DocumentationFailure(422);
			return spec;
		},
	});
	if (query.isPending)
		return (
			<Panel>
				<Spinner label="Loading API documentation" />
			</Panel>
		);
	if (query.isError) {
		const status =
			query.error instanceof DocumentationFailure ? query.error.status : 0;
		if (status === 404)
			return (
				<Panel>
					<EmptyState
						heading="Documentation is not published yet"
						description="Ask your administrator to publish the API documentation for this installation."
					/>
				</Panel>
			);
		const denied = status === 401 || status === 403;
		return (
			<Panel className="space-y-4">
				<ErrorState
					title="Couldn't load API documentation"
					detail={
						denied
							? "You don't have access to this documentation. Ask your administrator to grant access."
							: "The documentation could not be loaded. Your administrator can help if this continues."
					}
				/>
				{!denied && (
					<Button variant="outline" onClick={() => void query.refetch()}>
						Try again
					</Button>
				)}
			</Panel>
		);
	}
	return (
		<Panel className="min-w-0 overflow-auto p-0">
			<SwaggerUI spec={query.data} />
		</Panel>
	);
}
