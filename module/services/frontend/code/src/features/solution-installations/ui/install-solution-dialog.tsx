"use client";

import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
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
	choicesMatch,
	useInstallationTeamChoices,
} from "../service/team-choices";
import { DeclaredAccess } from "./declared-access";
import { InstallationTeamChoices } from "./installation-team-choices";

const offers = createClient(OfferService, apiTransport);

/** The cached review survives dismissal; only an explicit new review replaces it. */
export function InstallSolutionDialog({
	orgId,
	solution,
	onSubmit,
	isPending,
	error,
	onClose,
}: {
	orgId: string;
	solution: AvailableSolution;
	onSubmit: (review: ReviewedSolutionInstall) => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
}) {
	const service = useInstallationService();
	const teamChoices = useInstallationTeamChoices(orgId, solution.targetId);
	const [teamBusy, setTeamBusy] = useState(false);
	const { user } = useAuth();
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
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[640px]">
				<DialogHeader>
					<DialogTitle>Install {solution.routeAlias}</DialogTitle>
					<DialogDescription>
						{data?.consent.release ?? releaseText(solution)}. Review the
						solution and its supporting modules together.
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
							subject="solution"
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
						<InstallationTeamChoices
							orgId={orgId}
							targetId={solution.targetId}
							offer={data.offer}
							disabled={isPending}
							onBusyChange={setTeamBusy}
						/>
						<label className="flex items-start gap-2">
							<input
								type="checkbox"
								checked={approved}
								onChange={(event) => setApproved(event.target.checked)}
								disabled={isPending}
							/>
							I approve this solution and the required module access shown
							above.
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
						disabled={
							!data ||
							!approved ||
							isPending ||
							teamBusy ||
							teamChoices.loading ||
							!choicesMatch(teamChoices.value, data.offer.approval)
						}
						onClick={() => {
							if (data) {
								setSubmitted(true);
								// Staged team choices ride on this Install's consent and are
								// granted only once the installation it creates is active.
								teamChoices.authorize(data.consent.approval?.operationId ?? "");
								onSubmit(data.consent);
							}
						}}
					>
						{isPending
							? "Installing…"
							: submitted || error
								? "Retry approved installation"
								: "Install"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
