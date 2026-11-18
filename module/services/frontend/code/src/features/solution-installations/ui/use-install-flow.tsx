"use client";

import { useRouter } from "next/navigation";
import { type ReactNode, useState } from "react";
import { toast } from "sonner";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { failureText } from "@/shared/lib/failure-text";
import { hostRefusal } from "../model/installations";
import { type PageAccess, setUpRoles } from "../model/listing";
import { installOutcome, setUpOutcome } from "../model/outcomes";
import {
	useInstallReviewedSolution,
	useSetUpModulePage,
} from "../service/mutations";
import { InstallSolutionDialog } from "./install-solution-dialog";
import { SetUpModulePageDialog } from "./set-up-module-page-dialog";

/**
 * The three reviewed acts the catalogue starts — install a solution, update
 * an installed one, set up access — and the dialog each is reviewed in.
 *
 * An install submits the host's own review unchanged and then opens the new
 * installation's page; an update installs the reviewed release on the same
 * installation, which keeps its teams; Set up gives each role its team in one
 * act. What the host could not apply is said, never passed off as done.
 */
export function useInstallFlow({
	orgId,
	chosen,
	accessOf,
}: {
	orgId: string;
	/** Whether a target is one an administrator installs. */
	chosen: (target: AvailableSolution | undefined) => boolean;
	accessOf: (target: AvailableSolution) => PageAccess;
}): {
	/** A target is under install review. */
	reviewingInstall: boolean;
	/** An install or an update is being applied. */
	isApplying: boolean;
	install: (target: AvailableSolution) => void;
	update: (target: AvailableSolution) => void;
	setUp: (target: AvailableSolution) => void;
	dialogs: ReactNode;
} {
	const router = useRouter();
	const apply = useInstallReviewedSolution(orgId);
	const setUpAccess = useSetUpModulePage(orgId);
	const [installing, setInstalling] = useState<AvailableSolution | null>(null);
	// The installed target an administrator is updating: a newer release the
	// host offers on its existing installation, reviewed as an install is.
	const [updating, setUpdating] = useState<AvailableSolution | null>(null);
	const [settingUp, setSettingUp] = useState<AvailableSolution | null>(null);
	// The roles that Set up gives teams, taken when it is opened: once grants
	// are read again, an open dialog still shows what it was asked to set up.
	const [settingUpRoles, setSettingUpRoles] = useState<string[]>([]);
	const applyError = apply.isError ? hostRefusal(apply.error) : undefined;

	const dialogs = (
		<>
			{updating && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={updating}
					upgrade
					isPending={apply.isPending}
					error={applyError}
					onSubmit={(review, access) =>
						apply.mutate(
							{ review, access },
							{
								onSuccess: () => {
									toast.success(
										`${updating.routeAlias} updated. Its team access is kept.`,
									);
									setUpdating(null);
								},
								onError: (error) =>
									toast.error(
										failureText(`update ${updating.routeAlias}`, error),
									),
							},
						)
					}
					onClose={() => setUpdating(null)}
				/>
			)}
			{settingUp && (
				<SetUpModulePageDialog
					orgId={orgId}
					page={settingUp}
					roles={settingUpRoles}
					isPending={setUpAccess.isPending}
					error={
						setUpAccess.isError ? hostRefusal(setUpAccess.error) : undefined
					}
					onSubmit={(access) =>
						setUpAccess.mutate(
							{ targetId: settingUp.targetId, access },
							{
								onSuccess: (installation) => {
									const outcome = setUpOutcome(
										settingUp.routeAlias,
										installation,
									);
									toast[outcome.kind](outcome.text);
									setSettingUp(null);
								},
							},
						)
					}
					onClose={() => setSettingUp(null)}
				/>
			)}
			{installing && chosen(installing) && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={installing}
					isPending={apply.isPending}
					error={applyError}
					onSubmit={(review, access) =>
						apply.mutate(
							{ review, access },
							{
								onSuccess: (installation) => {
									const outcome = installOutcome(installation);
									toast[outcome.kind](outcome.text);
									setInstalling(null);
									router.push(`/admin/solutions/${installation.id}`);
								},
								onError: (error) =>
									toast.error(
										failureText(`install ${installing.routeAlias}`, error),
									),
							},
						)
					}
					onClose={() => setInstalling(null)}
				/>
			)}
		</>
	);

	return {
		reviewingInstall: installing !== null,
		isApplying: apply.isPending,
		install: (target) => {
			apply.reset();
			setInstalling(target);
		},
		update: (target) => {
			apply.reset();
			setUpdating(target);
		},
		setUp: (target) => {
			setUpAccess.reset();
			setSettingUpRoles(setUpRoles(target, accessOf(target)));
			setSettingUp(target);
		},
		dialogs,
	};
}
