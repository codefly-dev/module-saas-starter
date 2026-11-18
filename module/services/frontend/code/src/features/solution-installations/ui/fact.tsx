import { Badge } from "@/shared/ui";
import type { FactView } from "../model/installations";

/**
 * One fact. A gap reads as its label in muted italics with what it means on
 * hover, so "this host does not know" is never mistaken for a value.
 *
 * One renderer for the listing and the setup page, which state the same facts
 * about an installation: a second would be a second answer to "is this a value
 * or a gap", and the two would drift.
 */
export function Fact({ view }: { view: FactView }) {
	if (view.kind === "gap") {
		return (
			<span
				className="text-sm italic text-muted-foreground"
				title={view.detail}
			>
				{view.label}
			</span>
		);
	}
	if (view.tone) {
		return (
			<Badge tone={view.tone} dot title={view.detail}>
				{view.text}
			</Badge>
		);
	}
	return (
		<span className="text-sm" title={view.detail}>
			{view.text}
			{view.detail && (
				<span className="block text-xs text-muted-foreground">
					{view.detail}
				</span>
			)}
		</span>
	);
}
