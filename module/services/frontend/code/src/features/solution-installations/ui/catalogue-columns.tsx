import { type ColumnDef, createColumnHelper } from "@tanstack/react-table";
import Link from "next/link";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { Badge, Button, buttonVariants } from "@/shared/ui";
import type { CatalogueRow } from "../model/listing";
import { TargetName } from "./target-name";

/**
 * What the catalogue's cells need beside their row. Passed as the table's
 * `meta` so the column definitions stay module constants: a cell defined per
 * render is a new component per render, and its buttons would lose focus.
 */
export interface CatalogueMeta {
	titles: ReadonlyMap<string, string>;
	/** An install is being reviewed or applied: no second one is offered. */
	installing: boolean;
	onInstall: (target: AvailableSolution) => void;
	onSetUp: (target: AvailableSolution) => void;
}

const col = createColumnHelper<CatalogueRow>();

const meta = (table: { options: { meta?: unknown } }) =>
	table.options.meta as CatalogueMeta;

const name = (header: string) =>
	col.display({
		id: "name",
		header,
		cell: ({ row, table }) => (
			<span className="font-medium">
				<TargetName alias={row.original.alias} titles={meta(table).titles} />
			</span>
		),
	});

const state = col.display({
	id: "state",
	header: "State",
	cell: ({ row }) => {
		const { text, tone } = row.original.state;
		return tone ? (
			<Badge tone={tone} dot>
				{text}
			</Badge>
		) : (
			<Badge variant="outline">{text}</Badge>
		);
	},
});

const action = col.display({
	id: "action",
	header: "Action",
	cell: ({ row, table }) => {
		const { action, alias, solution } = row.original;
		const { installing, onInstall, onSetUp } = meta(table);
		switch (action.kind) {
			case "manage":
				return (
					<Link
						href={`/admin/solutions/${action.installationId}`}
						aria-label={`Manage access to ${alias}`}
						className={buttonVariants({ variant: "outline", size: "sm" })}
					>
						Manage access
					</Link>
				);
			case "setUp":
				return (
					<Button
						variant="outline"
						size="sm"
						aria-label={`Set up access to ${alias}`}
						onClick={() => onSetUp(solution)}
					>
						Set up
					</Button>
				);
			case "install":
				return (
					<Button
						variant="outline"
						size="sm"
						disabled={installing}
						aria-label={`Install ${alias}`}
						onClick={() => onInstall(solution)}
					>
						Install
					</Button>
				);
			default:
				return null;
		}
	},
});

const version = col.display({
	id: "version",
	header: "Version",
	cell: ({ row }) => (
		<span className="text-sm" title={row.original.release}>
			{row.original.solution.releaseVersion}
		</span>
	),
});

type Columns = ColumnDef<CatalogueRow>[];

/** Admin → Solutions: what can be installed, at which version. */
export const SOLUTION_COLUMNS: Columns = [
	name("Solution"),
	version,
	state,
	action,
];

/** Admin → Solutions: the module pages whose access is set up. */
export const MODULE_PAGE_COLUMNS: Columns = [
	name("Module page"),
	version,
	state,
	action,
];

/** Supporting module administration: the release in use; nothing to act on. */
export const MODULE_COLUMNS: Columns = [
	name("Module or solution"),
	col.display({
		id: "release",
		header: "Release",
		cell: ({ row }) => (
			<span className="font-mono text-sm">{row.original.release}</span>
		),
	}),
	col.display({
		id: "generation",
		header: "Generation",
		cell: ({ row }) => (
			<span className="text-sm text-muted-foreground">
				{row.original.generation}
			</span>
		),
	}),
	state,
];
