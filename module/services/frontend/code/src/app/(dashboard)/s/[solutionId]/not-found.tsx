import { PackageX } from "lucide-react";
import Link from "next/link";
import { EmptyState } from "@/components/empty-state";
import { buttonVariants, Page, PageHeader } from "@/shared/ui";

/**
 * An address nothing is registered at: no solution and no module page under
 * that id, or one registered but not serving. A real 404. A registered page the
 * viewer is not admitted to is a different answer with its own state
 * (PageNotAdmitted), never this one. Rendered inside the product shell, so the
 * navigation, which lists every page that IS available, stays in reach. A
 * registry this host cannot read is not this page: that is an error, because
 * the page may well exist.
 */
export default function SolutionNotFound() {
	return (
		<Page>
			<PageHeader title="Page not found" />
			<EmptyState
				headingAs="h2"
				icon={PackageX}
				title="No page at this address"
				description="No solution or module page is served at this address."
				action={
					<Link href="/" className={buttonVariants({ variant: "outline" })}>
						Go to the dashboard
					</Link>
				}
			/>
		</Page>
	);
}
