import { PageHeader, Panel } from "@/shared/ui";

/**
 * What a page shows a person who may not open it: who may, in the page's own
 * words. One component, so every refused page reads and looks the same.
 */
export function AccessDenied({
	title,
	description,
}: {
	title: string;
	description: string;
}) {
	return (
		<Panel className="p-6">
			<PageHeader title={title} description={description} />
		</Panel>
	);
}
