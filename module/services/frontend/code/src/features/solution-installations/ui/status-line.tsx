import Link from "next/link";
import { Badge, ChipGroup } from "@/shared/ui";
import { installedName, statusLine } from "../model/access";
import type { InstalledRow } from "../model/installations";

/**
 * Where one installation stands, on one line: the host's status, readiness
 * and health, an update the authority offers, and what it cannot reach now.
 * Anything that needs an administrator links to the installation's page,
 * which lists why.
 */
export function StatusLine({
	row,
	unreached,
}: {
	row: InstalledRow;
	unreached: readonly string[];
}) {
	return (
		<ChipGroup
			label={`Status of ${installedName(row)}`}
			className="max-w-xs gap-1"
		>
			{statusLine(row, unreached).map((fact) =>
				fact.tone === "success" ? (
					<Badge key={fact.text} tone="success" dot>
						{fact.text}
					</Badge>
				) : (
					<Link
						key={fact.text}
						href={`/admin/solutions/${row.installationId}`}
						className="hover:underline"
					>
						<Badge
							tone={fact.tone}
							dot
							className="h-auto whitespace-normal text-left"
						>
							{fact.text}
						</Badge>
					</Link>
				),
			)}
		</ChipGroup>
	);
}
