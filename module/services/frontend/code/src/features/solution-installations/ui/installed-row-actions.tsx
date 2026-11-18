import Link from "next/link";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installations_pb";
import { Button, buttonVariants } from "@/shared/ui";
import type { InstalledRow } from "../model/installations";

/** Which acts are in flight, so a row offers none that would race one. */
export interface InstalledBusy {
	retrying: boolean;
	uninstalling: boolean;
	updating: boolean;
	granting: boolean;
}

/**
 * What an administrator can do with one installed row: retry a failed or
 * stalled install, review an update the host offers, open its access, and
 * uninstall a solution (a module is installed by composition; only its
 * delivered withdrawal retires it).
 */
export function InstalledRowActions({
	row,
	name,
	busy,
	onRetry,
	onUpdate,
	onUninstall,
}: {
	row: InstalledRow;
	name: string;
	busy: InstalledBusy;
	onRetry: (row: InstalledRow) => void;
	onUpdate: (target: AvailableSolution) => void;
	/** Asks before uninstalling: the page confirms, then uninstalls. */
	onUninstall: (row: InstalledRow) => void;
}) {
	const retryable =
		row.status === InstallationStatus.FAILED ||
		(row.status === InstallationStatus.PENDING && !row.hasHostScopeRoot);
	const target = row.target;
	return (
		<div className="flex flex-wrap items-center justify-end gap-2">
			{retryable && (
				<Button
					variant="outline"
					size="sm"
					disabled={busy.retrying || busy.uninstalling}
					aria-label={`Retry ${name}`}
					onClick={() => onRetry(row)}
				>
					Retry
				</Button>
			)}
			{row.upgradeAvailable &&
				target &&
				row.status === InstallationStatus.ACTIVE && (
					<Button
						variant="outline"
						size="sm"
						disabled={busy.updating || busy.uninstalling}
						aria-label={`Update ${name}`}
						onClick={() => onUpdate(target)}
					>
						Review update
					</Button>
				)}
			<Link
				href={`/admin/solutions/${row.installationId}`}
				aria-label={`Manage access to ${name}`}
				className={buttonVariants({ variant: "outline", size: "sm" })}
			>
				Manage access
			</Link>
			{target?.kind !== "module" && (
				<Button
					variant="ghost"
					size="sm"
					className="text-destructive"
					disabled={busy.uninstalling}
					aria-label={`Uninstall ${name}`}
					onClick={() => onUninstall(row)}
				>
					Uninstall
				</Button>
			)}
		</div>
	);
}
