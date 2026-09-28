"use client";

import type { ReactNode } from "react";
import { cn } from "./util.js";

/**
 * "You are not allowed to see this", for a source's execution view.
 *
 * The durable work behind a sync is started by the module that ingests the
 * source, so it belongs to that module's principal rather than to the person
 * reading this panel. A viewer whose permission only covers *their own* runs
 * therefore gets a successful, empty answer — and an empty execution view sits
 * directly beneath a panel whose own empty state says the source has never
 * synced. Two correct components, one false statement.
 *
 * So a consumer whose viewer cannot read the organization's runs renders this
 * instead of an empty result. It exists in the kit rather than in each consumer
 * so the sentence is the same wherever it appears, and so that "say it
 * explicitly" is a component call rather than a thing each consumer has to
 * remember to write.
 *
 * It names no module and no permission: which authority governs this is the
 * consumer's to know, and this package may not name the module that runs the
 * work. `children` is where a consumer adds its own specifics if it has any.
 */
export function SourceExecutionRestricted({
	children,
	className,
}: {
	children?: ReactNode;
	className?: string;
}) {
	return (
		<div
			data-slot="source-execution-restricted"
			role="status"
			className={cn("space-y-1 type-body text-muted-foreground", className)}
		>
			<p>Sync runs are visible to organization administrators.</p>
			{children}
		</div>
	);
}
