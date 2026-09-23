import { PackageX } from "lucide-react";
import Link from "next/link";
import { EmptyState } from "@/components/empty-state";
import { buttonVariants } from "@/shared/ui";

/**
 * A solution slug the registry does not know: none registered under that id, or
 * one registered but not serving. Rendered inside the product shell, so the
 * navigation — which lists every solution that IS available — stays in reach.
 * A registry this host cannot read is not this page: that is an error, because
 * the solution may well exist.
 */
export default function SolutionNotFound() {
	return (
		<div className="p-6">
			<EmptyState
				icon={PackageX}
				title="Solution not found"
				description="No solution is available at this address. It may not be installed here, or it may not be running right now."
				action={
					<Link href="/" className={buttonVariants({ variant: "outline" })}>
						Go to the dashboard
					</Link>
				}
			/>
		</div>
	);
}
