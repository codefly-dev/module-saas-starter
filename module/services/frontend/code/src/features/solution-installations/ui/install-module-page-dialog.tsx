"use client";

import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { apiTransport } from "@/lib/connect/transport";
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
import { offerScopeText } from "../model/install-review";
import { hostRefusal, releaseText } from "../model/installations";
import { DeclaredAccess } from "./declared-access";

const offers = createClient(OfferService, apiTransport);

/**
 * The review of a module page that declares roles: what it receives and what
 * each of its roles permits, read from the authority's offer as a solution's
 * review is. A module stays installed by this host (the authority installs
 * only solutions), so Install carries no agent role: the module's signed
 * installation role decides. Teams take its roles in Set up, as a solution's do.
 */
export function InstallModulePageDialog({
	orgId,
	solution,
	onSubmit,
	isPending,
	error,
	onClose,
}: {
	orgId: string;
	solution: AvailableSolution;
	onSubmit: () => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
}) {
	const [approved, setApproved] = useState(false);
	const review = useQuery({
		queryKey: ["module-page-install-review", orgId, solution.targetId],
		queryFn: () =>
			offers.reviewOffer({
				organizationId: orgId,
				target: solution.routeAlias,
			}),
		staleTime: Infinity,
		retry: false,
		refetchOnWindowFocus: false,
		refetchOnReconnect: false,
	});
	const offer = !review.isError && !review.isFetching ? review.data : undefined;
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[640px]">
				<DialogHeader>
					<DialogTitle>Install {solution.routeAlias}</DialogTitle>
					<DialogDescription>
						{releaseText(solution)}. Review the module page and its roles.
					</DialogDescription>
				</DialogHeader>
				<DelayedLoading
					active={review.isFetching}
					label="Preparing the module's access"
				/>
				{review.isError && <p role="alert">{hostRefusal(review.error)}</p>}
				{offer && (
					<div className="space-y-4 text-sm">
						<DeclaredAccess
							subject="module"
							installationRole={offer.installationRole}
							offer={offer}
						/>
						<section aria-label="Roles" className="space-y-2">
							<h3 className="font-medium">Roles</h3>
							<ul className="list-disc pl-5">
								{offer.roles.map((role) => (
									<li key={role.name}>
										{role.name}:{" "}
										{role.scopes.map(offerScopeText).join("; ") ||
											"No data access declared."}
									</li>
								))}
							</ul>
							<p>
								Choose a team for each role in Set up once the module is
								installed. Nobody holds a role until it is granted there.
							</p>
						</section>
						<label className="flex items-start gap-2">
							<input
								type="checkbox"
								checked={approved}
								onChange={(event) => setApproved(event.target.checked)}
								disabled={isPending}
							/>
							I approve this module and the access shown above.
						</label>
					</div>
				)}
				{error && <p role="alert">The host refused the install: {error}</p>}
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button
						variant="outline"
						disabled={isPending || review.isFetching}
						onClick={() => {
							setApproved(false);
							void review.refetch();
						}}
					>
						Review again
					</Button>
					<Button
						disabled={!offer || !approved || isPending}
						onClick={onSubmit}
					>
						{isPending ? "Installing…" : "Install"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
