import type { ReactNode } from "react";
import { ListItem } from "./list.js";

export interface TimelineEntry {
	id: string;
	title: ReactNode;
	description?: ReactNode;
	/** Omit when no timestamp was recorded. The component never invents one. */
	time?: { dateTime: string; label: ReactNode };
	icon?: ReactNode;
	actions?: ReactNode;
}
/** Entries preserve caller order; spacing conveys sequence, never duration. */
export function Timeline({
	entries,
	label,
	className,
}: {
	entries: readonly TimelineEntry[];
	label: string;
	className?: string;
}) {
	return (
		<ol
			// biome-ignore lint/a11y/noRedundantRoles: Safari otherwise drops semantics for list-style:none.
			role="list"
			aria-label={label}
			data-slot="timeline"
			className={className}
			style={{
				display: "flex",
				flexDirection: "column",
				gap: "1rem",
				listStyle: "none",
				margin: 0,
				padding: 0,
			}}
		>
			{entries.map((entry) => (
				<ListItem
					key={entry.id}
					icon={entry.icon}
					description={entry.description}
					actions={entry.actions}
					meta={
						entry.time ? (
							<time dateTime={entry.time.dateTime}>{entry.time.label}</time>
						) : undefined
					}
				>
					{entry.title}
				</ListItem>
			))}
		</ol>
	);
}
