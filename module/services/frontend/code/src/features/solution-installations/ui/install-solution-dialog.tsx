"use client";

import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { teamQueries } from "@/features/teams/service/queries";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import type {
	AvailableSolution,
	ReviewedSolutionInstall,
} from "@/gen/saas/accounts/v1/installations_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { useInstallationService } from "@/lib/hooks/use-api-client";
import {
	Button,
	DelayedLoading,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/shared/ui";
import {
	assertMatchingOffer,
	moduleConsentText,
} from "../model/install-review";
import { hostRefusal, releaseText } from "../model/installations";
import {
	type InstallAccess,
	roleTeamsOf,
	teamAssignment,
} from "../model/team-assignment";
import { DeclaredAccess } from "./declared-access";
import { InstallationTeamAssignment } from "./installation-team-assignment";

const offers = createClient(OfferService, apiTransport);

/**
 * The review of a role-gated target before it is installed: a solution, or a
 * module page declaring roles, which the host reviews and installs alike. The
 * cached review survives dismissal; only an explicit new review replaces it.
 * With `upgrade`, the same review approves a newer release on the existing
 * installation: the host keeps the installation active and its team grants.
 * A fresh install also says who gets access: the host gives each declared
 * role to a team and the installer joins the teams of the roles they tick.
 */
export function InstallSolutionDialog({
	orgId,
	solution,
	onSubmit,
	isPending,
	error,
	onClose,
	upgrade = false,
}: {
	orgId: string;
	solution: AvailableSolution;
	onSubmit: (review: ReviewedSolutionInstall, access: InstallAccess) => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
	upgrade?: boolean;
}) {
	const service = useInstallationService();
	const { user } = useAuth();
	const [chosen, setChosen] = useState<Record<string, string>>({});
	const [untaken, setUntaken] = useState<ReadonlySet<string>>(new Set());
	const [approved, setApproved] = useState(false);
	const [submitted, setSubmitted] = useState(false);
	const review = useQuery({
		queryKey: ["solution-install-review", orgId, user?.id, solution.targetId],
		queryFn: async () => {
			const consent = await service.reviewSolutionInstall({
				orgId,
				targetId: solution.targetId,
			});
			const offer = await offers.reviewOffer({
				organizationId: orgId,
				target: consent.target,
			});
			assertMatchingOffer(consent, offer);
			const modules = consent.modules.map((module) => ({
				module,
				access: module.required ? moduleConsentText(module.scope) : [],
			}));
			return { consent, offer, modules };
		},
		staleTime: Infinity,
		retry: false,
		refetchOnWindowFocus: false,
		refetchOnReconnect: false,
	});
	const data = !review.isError && !review.isFetching ? review.data : undefined;
	const teams = useQuery({
		...teamQueries.list(orgId),
		enabled: !upgrade && Boolean(orgId),
	});
	const teamList = teams.data?.teams ?? [];
	const roles = data?.offer.roles.map((role) => role.name) ?? [];
	const target = data?.offer.target ?? "";
	const assignment = teamAssignment(target, roles, teamList, chosen);
	const access: InstallAccess = upgrade
		? { roleTeams: {}, installerRoles: [] }
		: {
				roleTeams: roleTeamsOf(assignment),
				installerRoles: roles.filter((role) => !untaken.has(role)),
			};
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[640px]">
				<DialogHeader>
					<DialogTitle>
						{upgrade ? "Update" : "Install"} {solution.routeAlias}
					</DialogTitle>
					<DialogDescription>
						{data?.consent.release ?? releaseText(solution)}.{" "}
						{upgrade
							? "This release asks more than the installed one was approved for. Review what it asks and its supporting modules together; the installation stays active and its team grants are kept."
							: "Review it and its supporting modules together."}
					</DialogDescription>
				</DialogHeader>
				<DelayedLoading
					active={review.isFetching}
					label="Preparing required access"
				/>
				{review.isError && <p role="alert">{hostRefusal(review.error)}</p>}
				{data && (
					<div className="space-y-4 text-sm">
						<DeclaredAccess
							subject={solution.kind === "module" ? "module" : "solution"}
							installationRole={data.consent.installationRole}
							offer={data.offer}
						/>
						<section aria-label="Supporting modules" className="space-y-3">
							<h3 className="font-medium">Supporting modules</h3>
							{data.modules.length === 0 && (
								<p>No supporting modules are required.</p>
							)}
							{data.modules.map(({ module, access }) => (
								<div
									key={module.principal}
									className="rounded border p-3 space-y-1"
								>
									<p className="font-medium">
										{module.target || module.principal}
									</p>
									{module.release && <p>{module.release}</p>}
									<p>
										{!module.required
											? "Optional — will not be installed."
											: module.reuseInstallationId
												? "Already installed — existing access will be reused unchanged."
												: "Required — will be installed with its own consent record."}
									</p>
									{module.required && (
										<>
											{!module.reuseInstallationId &&
												module.installationRole === "none" && (
													<p>No standing machine access is granted.</p>
												)}
											<ul className="list-disc pl-5">
												{access.map((line) => (
													<li key={line}>{line}</li>
												))}
											</ul>
										</>
									)}
								</div>
							))}
							<p>
								Supporting modules keep their own installation and revocation
								records. Uninstalling this solution keeps shared modules
								installed.
							</p>
						</section>
						{!upgrade && roles.length > 0 && (
							<InstallationTeamAssignment
								assignment={assignment}
								conventional={teamAssignment(target, roles, teamList, {})}
								teams={teamList}
								taken={new Set(access.installerRoles)}
								teamsError={
									teams.isError ? hostRefusal(teams.error) : undefined
								}
								disabled={isPending}
								onChoose={(role, teamId) =>
									setChosen(({ [role]: _, ...rest }) =>
										teamId ? { ...rest, [role]: teamId } : rest,
									)
								}
								onTake={(role, take) =>
									setUntaken((current) => {
										const next = new Set(current);
										if (take) next.delete(role);
										else next.add(role);
										return next;
									})
								}
							/>
						)}
						<label className="flex items-start gap-2">
							<input
								type="checkbox"
								checked={approved}
								onChange={(event) => setApproved(event.target.checked)}
								disabled={isPending}
							/>
							I approve this {upgrade ? "release" : "target"} and the required
							module access shown above.
						</label>
					</div>
				)}
				{error && (
					<p role="alert">
						{error} The original review is retained. Retry it after a temporary
						failure, or review again if access has changed.
					</p>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button
						variant="outline"
						disabled={isPending || review.isFetching}
						onClick={() => {
							setApproved(false);
							setSubmitted(false);
							void review.refetch();
						}}
					>
						Review again
					</Button>
					<Button
						disabled={!data || !approved || isPending}
						onClick={() => {
							if (data) {
								setSubmitted(true);
								// The host records this access with the installation and
								// applies it once setup is active, whoever resumes it.
								onSubmit(data.consent, access);
							}
						}}
					>
						{isPending
							? upgrade
								? "Updating…"
								: "Installing…"
							: submitted || error
								? upgrade
									? "Retry approved update"
									: "Retry approved installation"
								: upgrade
									? "Update"
									: "Install"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
