import { type ColumnDef, createColumnHelper } from "@tanstack/react-table";
import Link from "next/link";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installations_pb";
import { formatDate } from "@/shared/lib/utils";
import { type AccessView, installedName } from "../model/access";
import type { InstalledRow, TeamGrantRow } from "../model/installations";
import { Fact } from "./fact";
import {
	type InstalledBusy,
	InstalledRowActions,
} from "./installed-row-actions";
import { RoleHolders } from "./role-holders";
import { StatusLine } from "./status-line";
import { TargetName } from "./target-name";
import { TeamExposure } from "./team-exposure";

/**
 * What the installed list's cells need beside their row. Passed as the
 * table's `meta` so the column definitions stay module constants: a cell
 * defined per render is a new component per render, and a button pressed in
 * it would lose focus when the page re-renders.
 */
export interface InstalledMeta {
	titles: ReadonlyMap<string, string>;
	showModules: boolean;
	/** Who holds which role, and what each cannot reach, by installation. */
	access: AccessView;
	busy: InstalledBusy;
	onRetry: (row: InstalledRow) => void;
	onUpdate: (target: AvailableSolution) => void;
	onUninstall: (row: InstalledRow) => void;
	onGrant: (row: InstalledRow) => void;
	onRevoke: (row: InstalledRow, team: TeamGrantRow) => void;
}

const col = createColumnHelper<InstalledRow>();

const meta = (table: { options: { meta?: unknown } }) =>
	table.options.meta as InstalledMeta;

function NameCell({ row, meta }: { row: InstalledRow; meta: InstalledMeta }) {
	return (
		<div className="font-medium">
			<Link
				href={`/admin/solutions/${row.installationId}`}
				className="hover:underline"
			>
				{row.solution.kind === "value" ? (
					<TargetName alias={row.solution.text} titles={meta.titles} />
				) : (
					<Fact view={row.solution} />
				)}
			</Link>
			{meta.showModules ? (
				<span className="block font-normal">
					<Fact view={row.revision} />
				</span>
			) : (
				row.target && (
					<span
						className="block text-xs font-normal text-muted-foreground"
						title={
							row.revision.kind === "value" ? row.revision.text : undefined
						}
					>
						Version {row.target.releaseVersion}
					</span>
				)
			)}
			<span
				className="block text-xs font-normal text-muted-foreground"
				title={
					row.installedAt
						? undefined
						: "This installation was made before install dates were kept."
				}
			>
				{row.installedAt
					? `Installed ${formatDate(row.installedAt)}`
					: "Install date not recorded"}
			</span>
		</div>
	);
}

function AccessCell({ row, meta }: { row: InstalledRow; meta: InstalledMeta }) {
	if (row.status !== InstallationStatus.ACTIVE)
		return (
			<span className="text-sm text-muted-foreground">
				Given once it is installed
			</span>
		);
	if (row.hasHostScopeRoot)
		return (
			<TeamExposure
				row={row}
				name={installedName(row)}
				granting={meta.busy.granting}
				onGrant={() => meta.onGrant(row)}
				onRevoke={(team) => meta.onRevoke(row, team)}
			/>
		);
	if (meta.showModules)
		return <span className="text-sm text-muted-foreground">—</span>;
	return <RoleHolders row={row} access={meta.access} />;
}

const columns = (nameHeader: string): ColumnDef<InstalledRow>[] => [
	col.display({
		id: "name",
		header: nameHeader,
		cell: ({ row, table }) => (
			<NameCell row={row.original} meta={meta(table)} />
		),
	}),
	col.display({
		id: "status",
		header: "Status",
		cell: ({ row, table }) => (
			<>
				<StatusLine
					row={row.original}
					unreached={meta(table).access.unreached(row.original.installationId)}
				/>
				{row.original.failureReason && (
					<p className="mt-1 text-sm text-muted-foreground">
						{row.original.failureReason}
					</p>
				)}
			</>
		),
	}),
	col.display({
		id: "access",
		header: "Who has access",
		cell: ({ row, table }) => (
			<AccessCell row={row.original} meta={meta(table)} />
		),
	}),
	col.display({
		id: "action",
		header: "Action",
		cell: ({ row, table }) => {
			const { busy, onRetry, onUpdate, onUninstall } = meta(table);
			return (
				<InstalledRowActions
					row={row.original}
					name={installedName(row.original)}
					busy={busy}
					onRetry={onRetry}
					onUpdate={onUpdate}
					onUninstall={onUninstall}
				/>
			);
		},
	}),
];

/** Admin → Solutions. */
export const INSTALLED_SOLUTION_COLUMNS = columns("Solution or module page");
/** Supporting module administration. */
export const INSTALLED_MODULE_COLUMNS = columns("Module or solution");
