"use client";

import { DescriptionList, List, ListItem } from "@codefly-dev/ui/layout";
import { useState } from "react";
import { toast } from "sonner";
import type {
	GrantInstallationToTeamResponse,
	OrganizationInstallation,
} from "@/gen/saas/accounts/v1/installations_pb";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installations_pb";
import {
	Badge,
	Banner,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/ui";
import { hostRefusal, installedRows } from "../model/installations";
import {
	clearedByText,
	conditionHeading,
	entryKindText,
	reachOffers,
	readinessSummary,
} from "../model/readiness";
import { useGrantInstallationToTeam } from "../service/mutations";
import { Fact } from "./fact";
import { GrantTeamDialog } from "./grant-team-dialog";

/**
 * This host's own team access to one installation: its record's facts, the
 * teams granted here and those reaching it from above, what a grant carries
 * to the modules it reaches, and the act that grants a team. Shown only for
 * a record carrying a host scope root and no declared roles.
 */
export function HostTeamAccessCard({
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
	const name = row.target?.routeAlias || "this installation";
	const fact = installedRows([row])[0];
	const summary = readinessSummary(row.readiness);
	const offers = reachOffers(row.reachedModules);
	const grant = useGrantInstallationToTeam(orgId);
	const [granting, setGranting] = useState(false);
	const [refusal, setRefusal] = useState<string | null>(null);
	// What the last grant carried, as the host reported it in the grant's own
	// response. Kept rather than re-read: a module that holds nothing is the one
	// thing a person used to learn by hitting a Forbidden later, so it is shown
	// here, now, in the host's own words.
	const [carried, setCarried] =
		useState<GrantInstallationToTeamResponse | null>(null);

	return (
		<>
			<Card>
				<CardHeader>
					<CardTitle>Team access on this host</CardTitle>
					<CardDescription>
						This host&apos;s own installation record and the teams it admits to
						the solution, at which role. A team granted here holds everything
						that role permits beneath this installation&apos;s authority root; a
						team reaching it from a grant above is real reach that was not
						granted here. The solution&apos;s own roles are granted above.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					{refusal && (
						<Banner
							tone="danger"
							title={`Couldn't grant a team access to ${name}`}
							onDismiss={() => setRefusal(null)}
						>
							{refusal}
						</Banner>
					)}
					{fact && (
						<DescriptionList
							items={[
								{ term: "Release", value: <Fact view={fact.revision} /> },
								{ term: "Lifecycle", value: <Fact view={fact.lifecycle} /> },
								{ term: "Record health", value: <Fact view={fact.health} /> },
								{
									term: "Readiness",
									value: (
										<Badge tone={summary.tone} dot aria-label="Readiness">
											{summary.text}
										</Badge>
									),
								},
							]}
						/>
					)}
					{fact && fact.grantedTeams.length === 0 && (
						<p className="text-sm text-muted-foreground">
							No team is granted here, so no member reaches it through this
							installation.
						</p>
					)}
					{fact && fact.grantedTeams.length > 0 && (
						<List label="Teams granted here" variant="divided">
							{fact.grantedTeams.map((team) => (
								<ListItem key={team.key} meta={team.role}>
									{team.team}
								</ListItem>
							))}
						</List>
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
							{entryKindText(row, rolesDeclared)} {name} reaches{" "}
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
		</>
	);
}
