import { ConfirmDialog } from "@/shared/ui";
import {
	type AccessView,
	installedName,
	uninstallQuestion,
} from "../model/access";
import type { InstalledRow } from "../model/installations";
import type { TeamRemoval } from "./use-installed-acts";

/** Asked before a solution is uninstalled: who loses access, and what is kept. */
export function UninstallConfirmation({
	row,
	access,
	isPending,
	onConfirm,
	onCancel,
}: {
	/** The row being uninstalled; null while nothing is asked. */
	row: InstalledRow | null;
	access: AccessView;
	isPending: boolean;
	onConfirm: () => void;
	onCancel: () => void;
}) {
	const question = row ? uninstallQuestion(row, access) : undefined;
	return (
		<ConfirmDialog
			open={row !== null}
			onOpenChange={(open) => !open && onCancel()}
			title={question?.title ?? ""}
			description={question?.description ?? ""}
			confirmLabel="Uninstall"
			pendingLabel="Uninstalling…"
			isPending={isPending}
			onConfirm={onConfirm}
		/>
	);
}

/** Asked before a team's grant at an installation is removed. */
export function RemoveTeamConfirmation({
	removal,
	isPending,
	onConfirm,
	onCancel,
}: {
	removal: TeamRemoval | null;
	isPending: boolean;
	onConfirm: () => void;
	onCancel: () => void;
}) {
	const name = removal ? installedName(removal.row) : "";
	return (
		<ConfirmDialog
			open={removal !== null}
			onOpenChange={(open) => !open && onCancel()}
			title={removal ? `Remove ${removal.team.team} from ${name}?` : ""}
			description={
				removal
					? `Everyone in ${removal.team.team} loses the ${removal.team.role} access this grant gives them to ${name} at once. Access they hold another way stays, and you can grant the team again.`
					: ""
			}
			confirmLabel="Remove team"
			pendingLabel="Removing…"
			isPending={isPending}
			onConfirm={onConfirm}
		/>
	);
}
