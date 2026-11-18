import { List, ListItem } from "@codefly-dev/ui/layout";
import Link from "next/link";
import { type AccessView, holdersByRole, installedName } from "../model/access";
import type { InstalledRow } from "../model/installations";

/** Each role and who holds it, by name, as the authority's grants say. */
export function RoleHolders({
	row,
	access,
}: {
	row: InstalledRow;
	access: AccessView;
}) {
	if (access.failed.has(row.installationId))
		return (
			<span className="text-sm text-muted-foreground">
				Could not be read.{" "}
				<Link
					href={`/admin/solutions/${row.installationId}`}
					className="underline"
				>
					Open
				</Link>
			</span>
		);
	const grants = access.grants.get(row.installationId);
	if (!grants) return <span className="text-sm text-muted-foreground">…</span>;
	const roles = holdersByRole(
		access.roles(row.installationId),
		grants,
		access.name,
	);
	if (!roles.length)
		return <span className="text-sm text-muted-foreground">No roles</span>;
	return (
		<List label={`Who has access to ${installedName(row)}`} className="gap-0.5">
			{roles.map(({ role, holders }) => (
				<ListItem key={role}>
					<span className="text-muted-foreground">{role}:</span>{" "}
					{holders.length ? (
						holders.join(", ")
					) : (
						<span className="italic text-muted-foreground">nobody</span>
					)}
				</ListItem>
			))}
		</List>
	);
}
