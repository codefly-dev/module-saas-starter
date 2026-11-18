import Link from "next/link";
import { buttonVariants, ErrorState, Page, PageHeader } from "@/shared/ui";

/**
 * A page that IS registered here, shown to a viewer the entitled projection
 * does not name. It is a refusal and reads as one: it names the page, says
 * whether it is a module page or a solution, and says the viewer is not
 * admitted. It is never the not-found state, which is for an address nothing
 * is registered at.
 *
 * It states nothing the response did not already carry. The server render of a
 * registered page holds its title and kind for any signed-in viewer (see
 * SolutionLivePage), so naming them discloses no more than the 200 does. What
 * it does not say is which of the two reasons holds, because the projection
 * does not tell this host: an organization that has not installed the page and
 * a viewer who holds no role on it are one answer here, and stay one.
 */
export function PageNotAdmitted({
	title,
	kind,
}: {
	title: string;
	kind?: "module" | "solution";
}) {
	return (
		<Page>
			<PageHeader title={title} />
			<ErrorState
				title={`You don't have access to ${title}`}
				detail={
					kind === "module"
						? "Ask your organization administrator to give you or your team access to this module page."
						: "Ask your organization administrator to check that this solution is installed and give you or your team access."
				}
			/>
			<Link href="/" className={buttonVariants({ variant: "outline" })}>
				Go to the dashboard
			</Link>
		</Page>
	);
}
