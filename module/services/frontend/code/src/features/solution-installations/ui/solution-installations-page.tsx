"use client";

import { Layers, RefreshCw } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import type { ReactNode } from "react";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import {
	Button,
	DelayedLoading,
	EmptyState,
	Page as PageBody,
	PageHeader,
	Tabs,
	TabsList,
	TabsTrigger,
	useLoadingPhase,
} from "@/shared/ui";
import { CatalogueCard } from "./catalogue-card";
import { InstalledCard } from "./installed-card";
import { OffersReadBanner } from "./offers-read-banner";
import { useInstallFlow } from "./use-install-flow";
import { useSolutionsListing } from "./use-solutions-listing";

// Moved to the model and to its own file; exported here for the callers that
// learned them at this address.
export { type AccessView, uninstallLosers } from "../model/access";
export { Fact } from "./fact";

/**
 * The organization's modules and solutions: what it may install, what it has installed and
 * at which release, and the runtime acts an administrator performs on them —
 * install, expose to a team, uninstall. Each act is the host's to accept or
 * refuse, and a refusal is shown as the host words it.
 */
export function SolutionInstallationsPage({
	supportingModules = false,
}: {
	supportingModules?: boolean;
}) {
	const { organizationId: orgId = "" } = useAuth();
	// Keyed by organization: a draft or an open question never follows a
	// person into another organization.
	return (
		<SolutionsForOrganization
			key={orgId}
			orgId={orgId}
			showModules={supportingModules}
		/>
	);
}

/**
 * The Solutions route: solutions, or the supporting modules when the address
 * says `?view=modules`. One page, two views, so the navigation has one entry.
 */
export function SolutionInstallationsRoute() {
	const view = useSearchParams().get("view");
	return <SolutionInstallationsPage supportingModules={view === "modules"} />;
}

/** The page's title, what it is for, and the tab that chooses its view. */
function SolutionsHeader({
	showModules,
	actions,
}: {
	showModules: boolean;
	actions: ReactNode;
}) {
	const router = useRouter();
	return (
		<PageHeader
			className="flex-wrap"
			title={showModules ? "Supporting module administration" : "Solutions"}
			description={
				showModules
					? "Check the versions and health of the modules your platform provides."
					: "Review and install solutions for your organization, and choose which teams can use them."
			}
			actions={actions}
		>
			<Tabs
				value={showModules ? "modules" : "solutions"}
				onValueChange={(view) =>
					router.push(
						view === "modules"
							? "/admin/solutions?view=modules"
							: "/admin/solutions",
					)
				}
				className="mt-3"
			>
				<TabsList variant="line">
					<TabsTrigger value="solutions">Solutions</TabsTrigger>
					<TabsTrigger value="modules">Supporting modules</TabsTrigger>
				</TabsList>
			</Tabs>
		</PageHeader>
	);
}

function SolutionsForOrganization({
	orgId,
	showModules,
}: {
	orgId: string;
	showModules: boolean;
}) {
	const listing = useSolutionsListing(orgId, showModules);
	const { available, installed, offered } = listing;
	const flow = useInstallFlow({
		orgId,
		chosen: listing.chosen,
		accessOf: listing.accessOf,
	});

	return (
		<PageBody>
			<SolutionsHeader
				showModules={showModules}
				actions={
					<>
						<OrgSelector />
						{orgId && (
							<Button
								variant="outline"
								onClick={() => {
									void available.refetch();
									void installed.refetch();
								}}
								disabled={available.isFetching || installed.isFetching}
							>
								<RefreshCw className="h-4 w-4" />
								Refresh
							</Button>
						)}
					</>
				}
			/>
			{offered.isError && (
				<OffersReadBanner
					error={offered.error}
					showModules={showModules}
					retrying={offered.isFetching}
					onRetry={() => void offered.refetch()}
				/>
			)}
			{orgId ? (
				<SolutionsLists
					orgId={orgId}
					showModules={showModules}
					listing={listing}
					flow={flow}
				/>
			) : (
				<EmptyState
					variant="illustrated"
					icon={<Layers />}
					heading={
						showModules
							? "Select an organization to manage its modules and solutions"
							: "Select an organization to manage its solutions"
					}
					description="Installations belong to one organization at a time. Pick one from the selector above."
				/>
			)}
			<DelayedLoading
				active={(flow.reviewingInstall || showModules) && offered.isLoading}
				label="Reading what this organization can install"
			/>
			{flow.dialogs}
		</PageBody>
	);
}

/** The cards of the view on screen: its catalogue, then what is installed. */
function SolutionsLists({
	orgId,
	showModules,
	listing,
	flow,
}: {
	orgId: string;
	showModules: boolean;
	listing: ReturnType<typeof useSolutionsListing>;
	flow: ReturnType<typeof useInstallFlow>;
}) {
	const { available, installed, offered } = listing;
	return (
		<>
			{showModules ? (
				!listing.modulesWithheld && (
					<CatalogueCard
						variant="modules"
						listed={listing.listedModule}
						query={available}
						onInstall={flow.install}
						onSetUp={flow.setUp}
					/>
				)
			) : (
				<>
					<CatalogueCard
						variant="solutions"
						listed={listing.listedSolution}
						query={available}
						installing={
							(flow.reviewingInstall && offered.isPending) ||
							flow.isApplying ||
							!installed.isSuccess ||
							installed.isFetching
						}
						onInstall={flow.install}
						onSetUp={flow.setUp}
						accessOf={listing.accessOf}
					/>
					<CatalogueCard
						variant="modulePages"
						listed={listing.listedModulePage}
						query={available}
						onInstall={flow.install}
						onSetUp={flow.setUp}
						accessOf={listing.accessOf}
					/>
				</>
			)}
			<InstalledCard
				orgId={orgId}
				query={installed}
				showModules={showModules}
				unclassified={listing.modulesWithheld}
				listed={listing.listedInstalled}
				updating={flow.isApplying}
				access={listing.access}
				onUpdate={flow.update}
			/>
		</>
	);
}
