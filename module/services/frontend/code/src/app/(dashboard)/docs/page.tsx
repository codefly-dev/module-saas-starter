import { ApiDocsFrame } from "./api-docs-frame";

export default function DocsPage() {
	return (
		<div className="space-y-4">
			<div>
				<h1 data-slot="page-title" className="type-page-title">API Documentation</h1>
				<p className="text-muted-foreground">
					Interactive API reference powered by Swagger UI.
				</p>
			</div>
			<ApiDocsFrame />
		</div>
	);
}
