"use client";

import {
	RemoveHolder,
	roleLabel,
	type SetupSubject,
	SolutionRoleGrantForm,
} from "@codefly-dev/saas-ui/solution";
import { List, ListItem } from "@codefly-dev/ui/layout";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { InstallationService } from "@/gen/host/installation/v1/installation_pb";
import { apiTransport } from "@/lib/connect/transport";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Banner,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	DelayedLoading,
} from "@/shared/ui";
import { hostRefusal } from "../model/installations";
import { ListReadFailure } from "./list-read-state";
import { RolePanel } from "./review-section";

const installations = createClient(InstallationService, apiTransport);

/**
 * Who may use a module page, edited on its composition installation: the
 * authority refuses a module's offer read, so the roles come from the
 * installation read (`InstallationService.List`, the module's declared roles)
 * and every grant and revoke is the authority's `GrantRole` / `RevokeRole` on
 * that installation, as a solution's Set up makes them.
 */
export function ModulePageAccess({
	organizationId,
	installationId,
	target,
	title,
	subjects,
	teamControls,
}: {
	organizationId: string;
	installationId: string;
	target: string;
	/** How the host titles the page ("Example Pages"); each role reads as its ("Example Pages admin"). */
	title?: string;
	subjects: readonly SetupSubject[];
	/** The host's control for a team holding a role: its people, in place. */
	teamControls?: (team: SetupSubject) => ReactNode;
}) {
	const queryClient = useQueryClient();
	const key = ["module-page-access", organizationId, installationId];
	const read = useQuery({
		queryKey: key,
		queryFn: async () => {
			const listed = await installations.list({ organizationId });
			const installation = listed.installations.find(
				(i) => i.id === installationId,
			);
			const grants = await installations.listRoleGrants({
				organizationId,
				installationId,
			});
			return { roles: installation?.roles ?? [], grants: grants.grants };
		},
	});
	const change = useMutation({
		mutationFn: (request: Promise<unknown>) => request,
		onSettled: () =>
			Promise.all([
				queryClient.invalidateQueries({ queryKey: key }),
				queryClient.invalidateQueries({
					queryKey: ["installation-role-grants", organizationId],
				}),
			]),
	});
	const subject = (kind: "person" | "team", id: string): SetupSubject =>
		subjects.find((s) => s.kind === kind && s.id === id) ?? {
			kind,
			id,
			label: id,
		};
	return (
		<Card>
			<CardHeader>
				<CardTitle>Who has access to {target}</CardTitle>
				<CardDescription>
					{target} is part of the platform; its roles decide who can use it.
					Each role goes to teams or people, and anyone in a team has it.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-4">
				<DelayedLoading
					active={read.isLoading}
					label="Loading who has access"
				/>
				{read.isError && (
					<ListReadFailure
						outcome={readOutcome(true, read.error) as "forbidden" | "failed"}
						subject={`who has access to ${target}`}
						error={read.error}
						retrying={read.isFetching}
						onRetry={() => void read.refetch()}
					/>
				)}
				{change.isError && (
					<Banner tone="danger" title="Couldn't change who has access">
						{hostRefusal(change.error)}
					</Banner>
				)}
				{read.data?.roles.map((role) => {
					const holders = read.data.grants.filter((g) => g.role === role);
					return (
						<RolePanel
							key={role}
							role={role}
							heading={
								title
									? roleLabel(title, role)
									: `${role[0]?.toUpperCase()}${role.slice(1)}`
							}
						>
							<List label={`Who has ${roleLabel(title, role)}`}>
								{holders.length === 0 && (
									<ListItem className="text-muted-foreground">
										Nobody has it yet.
									</ListItem>
								)}
								{holders.map((g) => {
									const who = g.teamId
										? subject("team", g.teamId)
										: subject("person", g.personId);
									const name =
										who.kind === "team" ? `Team ${who.label}` : who.label;
									return (
										<ListItem
											key={`${g.teamId}:${g.personId}`}
											className="rounded-md bg-muted/40 p-2"
											description={
												who.kind === "team" ? teamControls?.(who) : undefined
											}
											actions={
												<RemoveHolder
													role={role}
													title={title}
													target={target}
													name={name}
													last={holders.length === 1}
													busy={change.isPending}
													onRemove={() =>
														change.mutate(
															installations.revokeRole({
																organizationId,
																installationId,
																role: g.role,
																personId: g.personId,
																teamId: g.teamId,
															}),
														)
													}
												/>
											}
										>
											{who.href ? (
												<a className="underline" href={who.href}>
													{name}
												</a>
											) : (
												name
											)}
										</ListItem>
									);
								})}
							</List>
							<SolutionRoleGrantForm
								roles={[role]}
								fixedRole={role}
								subjects={subjects}
								allowUnlistedSubjects={false}
								busy={change.isPending}
								submitLabel={`Give ${roleLabel(title, role)} access`}
								onGrant={({ personId, teamId }) =>
									change.mutate(
										installations.grantRole({
											organizationId,
											installationId,
											role,
											personId,
											teamId,
										}),
									)
								}
							/>
						</RolePanel>
					);
				})}
			</CardContent>
		</Card>
	);
}
