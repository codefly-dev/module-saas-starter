"use client";

import {
	type SetupSubject,
	SolutionRoleGrantForm,
} from "@codefly-dev/saas-ui/solution";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { InstallationService } from "@/gen/host/installation/v1/installation_pb";
import { apiTransport } from "@/lib/connect/transport";
import {
	Banner,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/ui";
import { hostRefusal } from "../model/installations";

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
	subjects,
	teamControls,
}: {
	organizationId: string;
	installationId: string;
	target: string;
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
				<CardTitle className="text-base">Who has access to {target}</CardTitle>
				<CardDescription>
					{target} is part of the platform; its roles decide who can use it.
					Each role goes to teams or people, and anyone in a team has it.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-4">
				{read.isError && (
					<Banner tone="danger" title="Its access could not be read">
						{hostRefusal(read.error)}
					</Banner>
				)}
				{change.isError && (
					<Banner tone="danger" title="The authority refused">
						{hostRefusal(change.error)}
					</Banner>
				)}
				{read.data?.roles.map((role) => {
					const holders = read.data.grants.filter((g) => g.role === role);
					return (
						<section
							key={role}
							aria-label={`Role ${role}`}
							className="space-y-2 rounded-lg border p-3"
						>
							<h4 className="font-medium">
								{role[0]?.toUpperCase()}
								{role.slice(1)}
							</h4>
							<ul aria-label={`Who has ${role}`} className="space-y-2">
								{holders.length === 0 && (
									<li className="text-muted-foreground">Nobody has it yet.</li>
								)}
								{holders.map((g) => {
									const who = g.teamId
										? subject("team", g.teamId)
										: subject("person", g.personId);
									const name =
										who.kind === "team" ? `Team ${who.label}` : who.label;
									return (
										<li
											key={`${g.teamId}:${g.personId}`}
											className="space-y-1 rounded-md bg-muted/40 p-2"
										>
											<div className="flex items-center justify-between gap-2">
												{who.href ? (
													<a className="underline" href={who.href}>
														{name}
													</a>
												) : (
													<span>{name}</span>
												)}
												<Button
													size="sm"
													variant="outline"
													aria-label={`Remove ${role} from ${name}`}
													disabled={change.isPending}
													onClick={() =>
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
												>
													Remove
												</Button>
											</div>
											{who.kind === "team" && teamControls?.(who)}
										</li>
									);
								})}
							</ul>
							<SolutionRoleGrantForm
								roles={[role]}
								fixedRole={role}
								subjects={subjects}
								allowUnlistedSubjects={false}
								busy={change.isPending}
								submitLabel={`Give ${role} access`}
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
						</section>
					);
				})}
			</CardContent>
		</Card>
	);
}
