"use client";

import { ArrowLeft, Layers, RefreshCw } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import type {
	GrantInstallationToTeamResponse,
	OrganizationInstallation,
} from "@/gen/saas/accounts/v1/installations_pb";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installations_pb";
import { useAuth } from "@/lib/auth";
import {
	Badge,
	Banner,
	Button,
	buttonVariants,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Spinner,
	useLoadingPhase,
} from "@/shared/ui";
import { hostRefusal, installedRows } from "../model/installations";
import {
	affordance,
	clearedByText,
	conditionHeading,
	entryKindText,
	reachOffers,
	readinessSummary,
} from "../model/readiness";
import {
	useGrantInstallationToTeam,
	useRetryInstallation,
} from "../service/mutations";
import { useInstallations } from "../service/queries";
import { GrantTeamDialog } from "./grant-team-dialog";
import { Fact } from "./solution-installations-page";

/**
 * One module's or one solution's setup page: what it is and whether it is ready,
 * what it still needs from a person, and who may reach it.
 *
 * It answers those three questions and nothing else. Installing, uninstalling
 * and the catalogue stay on `Admin → Modules & solutions`, which is where a
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
	const row = (query.data ?? []).find(
		(entry) => entry.installation?.id === installationId,
	);

	return (
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div className="space-y-1">
					<Link
						href="/admin/solutions"
						className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:underline"
					>
						<ArrowLeft className="h-3 w-3" />
						Modules &amp; solutions
					</Link>
					<h1 data-slot="page-title" className="type-page-title">
						{row?.target?.routeAlias || "This installation"}
					</h1>
					<p className="text-muted-foreground">
						{entryKindText(row)} — what it is, what it still needs before it
						will work, and who may reach it.
					</p>
				</div>
				<Button
					variant="outline"
					onClick={() => void query.refetch()}
					disabled={query.isFetching}
				>
					<RefreshCw className="h-4 w-4" />
					Refresh
				</Button>
			</div>

			{quiet ? null : indicator ? (
				<Spinner label="Loading this installation" />
			) : query.isError ? (
				<Banner tone="danger" title="This installation could not be read">
					{hostRefusal(query.error)}
				</Banner>
			) : !row ? (
				<EmptyState
					icon={Layers}
					title="This organization has no such installation"
					description="It may have been uninstalled, or it belongs to another organization. Go back to Modules & solutions."
				/>
			) : (
				<InstallationSetup orgId={orgId} row={row} />
			)}
		</div>
	);
}

function InstallationSetup({
	orgId,
	row,
}: {
	orgId: string;
	row: OrganizationInstallation;
}) {
	const installationId = row.installation?.id ?? "";
	const fact = installedRows([row])[0];
	const summary = readinessSummary(row.readiness);
	const offers = reachOffers(row.reachedModules);
	const retry = useRetryInstallation(orgId);
	const grant = useGrantInstallationToTeam(orgId);
	const [granting, setGranting] = useState(false);
	const [refusal, setRefusal] = useState<string | null>(null);
	// What the last grant carried, as the host reported it in the grant's own
	// response. Kept rather than re-read: a module that holds nothing is the one
	// thing a person used to learn by hitting a Forbidden later, so it is shown
	// here, now, in the host's own words.
	const [carried, setCarried] =
		useState<GrantInstallationToTeamResponse | null>(null);
	const name = row.target?.routeAlias || "this installation";

	return (
		<div className="space-y-6">
			{refusal && (
				<Banner
					tone="danger"
					title={`The host refused: ${name}`}
					onDismiss={() => setRefusal(null)}
				>
					{refusal}
				</Banner>
			)}

			<Card>
				<CardHeader>
					<CardTitle className="text-base">
						What it is, and is it ready?
					</CardTitle>
					<CardDescription>
						Readiness is about this {entryKindText(row).toLowerCase()} working.
						The record&apos;s own health — active, owned, granted — is one
						condition among the others below, not the answer.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					<dl className="grid gap-3 sm:grid-cols-3">
						<div>
							<dt className="text-xs uppercase text-muted-foreground">
								Release
							</dt>
							<dd className="mt-1">
								{fact ? <Fact view={fact.revision} /> : null}
							</dd>
						</div>
						<div>
							<dt className="text-xs uppercase text-muted-foreground">
								Lifecycle
							</dt>
							<dd className="mt-1">
								{fact ? <Fact view={fact.lifecycle} /> : null}
							</dd>
						</div>
						<div>
							<dt className="text-xs uppercase text-muted-foreground">
								Record health
							</dt>
							<dd className="mt-1">
								{fact ? <Fact view={fact.health} /> : null}
							</dd>
						</div>
					</dl>
					<Badge tone={summary.tone} dot aria-label="Readiness">
						{summary.text}
					</Badge>
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">
						What does it still need from a person?
					</CardTitle>
					<CardDescription>
						Each condition is in the words of whoever owns it, with who can
						clear it and what clears it — or plainly that nothing here can.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{summary.ready ? (
						<p className="py-4 text-sm text-muted-foreground">
							Nothing. Every condition this host can check is met.
						</p>
					) : (
						<ul className="space-y-4" aria-label="Unmet conditions">
							{(row.readiness?.unmet ?? []).map((condition) => {
								const act = affordance(condition);
								return (
									<li
										// The host sorts conditions by kind then subject, and a
										// source is identified by its id — so kind, subject and
										// source id name one condition and keep naming it across
										// refreshes.
										key={`${condition.kind}:${condition.subject}:${condition.sourceId}`}
										className="rounded-lg border p-3"
									>
										<p className="font-medium">
											{conditionHeading(condition.kind)}
											{condition.subject && (
												<span className="ml-1 font-mono text-sm text-muted-foreground">
													{condition.subject}
												</span>
											)}
										</p>
										<p className="mt-1 text-sm text-muted-foreground">
											{condition.detail}
										</p>
										<p className="mt-2 text-xs text-muted-foreground">
											Cleared by: {clearedByText(condition.clearedBy)}
										</p>
										<div className="mt-2">
											{act.kind === "perform" ? (
												<Button
													size="sm"
													variant="outline"
													disabled={retry.isPending}
													onClick={() => {
														setRefusal(null);
														retry.mutate(installationId, {
															onSuccess: () =>
																toast.success(`${name} installation pending`),
															onError: (error) =>
																setRefusal(hostRefusal(error)),
														});
													}}
												>
													{act.label}
												</Button>
											) : act.kind === "elsewhere" ? (
												<Link
													href={act.href}
													className={buttonVariants({
														variant: "outline",
														size: "sm",
													})}
												>
													{act.label}
												</Link>
											) : (
												<p className="text-sm italic text-muted-foreground">
													{act.label}
												</p>
											)}
										</div>
									</li>
								);
							})}
						</ul>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">Who may reach it?</CardTitle>
					<CardDescription>
						The teams a grant reaches here, at which role. A team granted here
						holds everything that role permits beneath this installation&apos;s
						authority root; a team reaching it from a grant above is real reach
						that was not granted here.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					{fact && fact.grantedTeams.length === 0 && (
						<p className="text-sm text-muted-foreground">
							No team is granted here, so no member reaches it through this
							installation.
						</p>
					)}
					{fact && fact.grantedTeams.length > 0 && (
						<ul className="space-y-1" aria-label="Teams granted here">
							{fact.grantedTeams.map((team) => (
								<li key={team.key} className="text-sm">
									<span className="font-medium">{team.team}</span>
									<span className="text-muted-foreground"> · {team.role}</span>
									<span className="ml-2 font-mono text-xs text-muted-foreground">
										{team.scopePath}
									</span>
								</li>
							))}
						</ul>
					)}
					{fact && fact.inheritedTeams.length > 0 && (
						<p className="text-sm text-muted-foreground">
							Inherited from above:{" "}
							{fact.inheritedTeams.map((team) => team.team).join(", ")}
						</p>
					)}
					{offers.length > 0 && (
						<Banner
							tone="info"
							title="A grant here also carries the modules this reaches"
						>
							{entryKindText(row)} {name} reaches{" "}
							{offers.map((offer) => offer.label).join(", ")} on the asking
							person&apos;s own session, from its own applied declaration. A
							grant carries each of them: what {name} declares it consumes
							there, intersected with the role you choose — never the whole role
							— written at that module&apos;s own authority root and revoked
							with this grant. There is no per-module step to perform.
						</Banner>
					)}
					{carried && (
						<Banner
							tone={carried.unmet.length > 0 ? "warning" : "success"}
							title="What that grant carried"
							onDismiss={() => setCarried(null)}
						>
							<div className="space-y-2">
								{carried.carried.length > 0 ? (
									<ul className="list-disc space-y-1 pl-4">
										{carried.carried.map((entry) => (
											<li key={entry.audience}>
												{entry.audience}: {entry.scopes.join(", ")}
											</li>
										))}
									</ul>
								) : (
									<p>It carried no module access.</p>
								)}
								{carried.unmet.map((condition) => (
									<p key={`${condition.kind}-${condition.subject}`}>
										<span className="font-medium">
											{conditionHeading(condition.kind)}
											{condition.subject ? ` — ${condition.subject}` : ""}:
										</span>{" "}
										{condition.detail} {clearedByText(condition.clearedBy)}
									</p>
								))}
							</div>
						</Banner>
					)}
					<Button
						variant="outline"
						size="sm"
						disabled={
							grant.isPending ||
							row.installation?.status !== InstallationStatus.ACTIVE
						}
						onClick={() => {
							grant.reset();
							setGranting(true);
						}}
					>
						Grant a team
					</Button>
					{row.installation?.status !== InstallationStatus.ACTIVE && (
						<p className="text-sm text-muted-foreground">
							A grant is available once the installation is active.
						</p>
					)}
				</CardContent>
			</Card>

			{granting && (
				<GrantTeamDialog
					orgId={orgId}
					solution={name}
					isPending={grant.isPending}
					error={grant.isError ? hostRefusal(grant.error) : undefined}
					onSubmit={({ teamId, roleId }) => {
						setRefusal(null);
						grant.mutate(
							{ installationId, teamId, roleId },
							{
								onSuccess: (response) => {
									toast.success(`Team grant recorded for ${name}`);
									setCarried(response);
									setGranting(false);
								},
								onError: (error) => setRefusal(hostRefusal(error)),
							},
						);
					}}
					onClose={() => setGranting(false)}
				/>
			)}
		</div>
	);
}
