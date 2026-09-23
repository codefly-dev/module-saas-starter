import { FileQuestion } from "lucide-react";
import Link from "next/link";
import { EmptyState } from "@/components/empty-state";
import { buttonVariants } from "@/shared/ui";

/**
 * The page every unmatched URL and every `notFound()` without a closer boundary
 * renders. Without it Next answers with its own unstyled default, outside the
 * product's shell and with no way back.
 */
export default function NotFound() {
	return (
		<main className="flex min-h-screen items-center justify-center p-6">
			<EmptyState
				icon={FileQuestion}
				title="Page not found"
				description="The page you asked for does not exist, or it has moved."
				action={
					<Link href="/" className={buttonVariants({ variant: "outline" })}>
						Go to the dashboard
					</Link>
				}
			/>
		</main>
	);
}
