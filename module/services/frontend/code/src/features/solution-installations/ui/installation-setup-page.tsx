"use client";

import type { SetupCondition } from "@codefly-dev/saas-ui/solution";
import { ArrowLeft, Layers, RefreshCw } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import type { OrganizationInstallation } from "@/gen/saas/accounts/v1/installations_pb";
import { useAuth } from "@/lib/auth";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	buttonVariants,
	EmptyState,
	Page as PageBody,
	PageHeader,
	Spinner,
	useLoadingPhase,
} from "@/shared/ui";
import { healthFacts } from "../model/access";
import { hostRefusal, isModulePage } from "../model/installations";
import {
	affordance,
	clearedByText,
	conditionHeading,
	entryKindText,
} from "../model/readiness";
import {
	useInstallReviewedSolution,
	useRetryInstallation,
} from "../service/mutations";
import {
	useAvailableSolutions,
	useInstallations,
	useUpdateAsks,
} from "../service/queries";
import { HostTeamAccessCard } from "./host-team-access-card";
import { InstallSolutionDialog } from "./install-solution-dialog";
import { ListReadFailure } from "./list-read-state";
import { SolutionSetupPanel } from "./solution-setup-panel";
import { TargetName, useTargetTitles } from "./target-name";

/**
 * One module's or one solution's setup page: what it is and whether it is ready,
 * what it still needs from a person, and who may reach it.
 *
 * It answers those three questions and nothing else. Installing, uninstalling
 * and the catalogue stay on `Admin → Solutions` (supporting modules on
 * `/admin/solutions?view=modules`), which is where a
 * person arrives from; this page exists because a row cannot carry a list of
 * unmet conditions, each with its own owner and its own act, for ten
 * installations at once — which is exactly the information that was missing.
 *
 * It reads the organisation's installation listing, the same read the row came
 * from, and selects its own installation out of it. One read serves both: a
 * second, per-installation read would be a second answer to the same question,
 * and the page would be able to disagree with the row that linked to it.
 */
export function InstallationSetupPage({
	installationId,
}: {
	installationId: string;
}) {
	const { organizationId: orgId = "" } = useAuth();
	const query = useInstallations(orgId);
	const { indicator, quiet } = useLoadingPhase(query.isLoading);
	const denied = readOutcome(query.isError, query.error) === "forbidden";
	const row = (denied ? [] : (query.data ?? [])).find(
		(entry) => entry.installation?.id === installationId,
	);
	// Where this installation is listed follows what it declares, not its
	// kind: a solution and a module page (a module declaring roles,
	// AvailableSolution.roles) sit on Solutions; a module declaring none is a
	// supporting module. The one catalogue read serves the header and the
	// setup below.
	const available = useAvailableSolutions(orgId);
	const rolesDeclared =
		row?.target?.kind === "solution"
			? true
			: row?.target && available.isSuccess
				? isModulePage(
						available.data.find((t) => t.targetId === row.target?.targetId),
					)
				: undefined;
	// Only a module the authority has answered "no roles" for is a supporting
	// module; an unknown or still-loading row, and every solution, belongs to
	// Solutions, which is also where the not-found state sends a person back.
	const supporting = row?.target?.kind === "module" && rolesDeclared === false;
	const titles = useTargetTitles();

	return (
		<PageBody>
			<PageHeader
				className="flex-wrap"
				title={
					row?.target?.routeAlias ? (
						<TargetName alias={row.target.routeAlias} titles={titles} />
					) : (
						"This installation"
					)
				}
				description={
					<>
						{entryKindText(row, rolesDeclared)} —{" "}
						{row?.target?.kind === "module"
							? "who has each of its roles."
							: "where it stands, what each role can do, and who has it."}
					</>
				}
				actions={
					denied ? undefined : (
						<Button
							variant="outline"
							onClick={() => void query.refetch()}
							disabled={query.isFetching}
						>
							<RefreshCw className="h-4 w-4" />
							Refresh
						</Button>
					)
				}
			>
				<Link
					href={
						supporting ? "/admin/solutions?view=modules" : "/admin/solutions"
					}
					className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:underline"
				>
					<ArrowLeft className="h-3 w-3" />
					{supporting ? "Supporting modules" : "Solutions"}
				</Link>
			</PageHeader>

			{quiet ? null : indicator ? (
				<Spinner label="Loading this installation" />
			) : !orgId ? (
				<EmptyState
					variant="illustrated"
					icon={<Layers />}
					heading="Select an organization to see this installation"
					description="An installation belongs to one organization. Choose yours, then open it from Solutions."
				/>
			) : query.isError && !row ? (
				<ListReadFailure
					outcome={readOutcome(true, query.error) as "forbidden" | "failed"}
					subject="this installation"
					error={query.error}
					retrying={query.isFetching}
					onRetry={() => void query.refetch()}
				/>
			) : query.isPending ? null : !row ? (
				<EmptyState
					variant="illustrated"
					icon={<Layers />}
					heading="This organization has no such installation"
					description="It may have been uninstalled, or it belongs to another organization."
				>
					<Link
						href="/admin/solutions"
						className={buttonVariants({ variant: "outline" })}
					>
						Back to Solutions
					</Link>
				</EmptyState>
			) : (
				<InstallationSetup
					orgId={orgId}
					row={row}
					rolesDeclared={rolesDeclared}
				/>
			)}
		</PageBody>
	);
}

function InstallationSetup({
	orgId,
	row,
	rolesDeclared,
}: {
	orgId: string;
	row: OrganizationInstallation;
	/** Whether the target declares roles, from the page's one read of them. */
	rolesDeclared: boolean | undefined;
}) {
	const installationId = row.installation?.id ?? "";
	const retry = useRetryInstallation(orgId);
	// An update the authority offers, on a solution or on a module page (held
	// at its approved reach the same way): what it asks, and its review here.
	const upgradeAvailable =
		row.installation?.upgradeAvailable === true && row.target !== undefined;
	const asks = useUpdateAsks(
		orgId,
		row.target?.targetId ?? "",
		upgradeAvailable,
	);
	const update = useInstallReviewedSolution(orgId);
	const [updating, setUpdating] = useState(false);
	const name = row.target?.routeAlias || "this installation";
	const target = row.target?.routeAlias ?? "";
	// This host's own team access card is for a record carrying a host scope
	// root and no declared roles: a module page that declares roles is set up
	// through them alone, as a solution is, so it offers no second grant form.
	const hasHostScopeRoot =
		Boolean(row.rootScopePath) &&
		(row.target?.kind !== "module" || rolesDeclared === false);
	// The host's own unmet conditions, in the words of whoever owns each, handed
	// to the shared Setup UI: it renders them beside the authority's answers.
	const conditions: SetupCondition[] = (row.readiness?.unmet ?? []).map(
		(condition) => {
			const act = affordance(condition);
			return {
				// The host sorts conditions by kind then subject, and a source is
				// identified by its id — so kind, subject and source id name one
				// condition and keep naming it across refreshes.
				key: `${condition.kind}:${condition.subject}:${condition.sourceId}`,
				heading: conditionHeading(condition.kind),
				subject: condition.subject || undefined,
				detail: condition.detail,
				clearedBy: clearedByText(condition.clearedBy),
				action:
					act.kind === "perform"
						? {
								kind: "perform",
								label: act.label,
								onPerform: () =>
									retry.mutateAsync(installationId).then(() => {
										toast.success(`${name} installation pending`);
									}),
							}
						: act.kind === "elsewhere"
							? { kind: "link", label: act.label, href: act.href }
							: { kind: "note", label: act.label },
			};
		},
	);

	return (
		<div className="space-y-6">
			<SolutionSetupPanel
				target={target}
				conditions={conditions}
				installation={row.installation}
				modulePage={row.target?.kind === "module" && rolesDeclared === true}
				facts={healthFacts(row.health)}
				update={
					upgradeAvailable
						? {
								asks: asks.data?.asks,
								approved: asks.data?.approved,
								action: (
									<div>
										<Button
											variant="outline"
											size="sm"
											disabled={update.isPending}
											onClick={() => {
												update.reset();
												setUpdating(true);
											}}
										>
											Review update
										</Button>
									</div>
								),
							}
						: undefined
				}
			/>
			{updating && row.target && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={row.target}
					upgrade
					isPending={update.isPending}
					error={update.isError ? hostRefusal(update.error) : undefined}
					onSubmit={(review, access) =>
						update.mutate(
							{ review, access },
							{
								onSuccess: () => {
									toast.success(`${name} updated. Everyone keeps their role.`);
									setUpdating(false);
								},
							},
						)
					}
					onClose={() => setUpdating(false)}
				/>
			)}

			{hasHostScopeRoot ? (
				<HostTeamAccessCard
					orgId={orgId}
					row={row}
					rolesDeclared={rolesDeclared}
				/>
			) : (
				<p className="text-sm text-muted-foreground">
					To give someone a role, add them to a team that has it, or give the
					role to them or their team above. What they already hold in other
					modules does not change.
				</p>
			)}
		</div>
	);
}
