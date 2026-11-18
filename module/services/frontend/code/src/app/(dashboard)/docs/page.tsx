import { Page as PageBody, PageHeader } from "@/shared/ui";
import { ApiDocsFrame } from "./api-docs-frame";

export default function DocsPage() {
	return (
		<PageBody>
			<PageHeader
				title="API documentation"
				description="Every request the API accepts and what it answers, which you can try from this page."
			/>
			<ApiDocsFrame />
		</PageBody>
	);
}
