"use client";

import {
	type SetupSubject,
	SolutionRoleGrantForm,
} from "@codefly-dev/saas-ui/solution";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
}: {
	organizationId: string;
	installationId: string;
	target: string;
	subjects: readonly SetupSubject[];
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
		onSettled: () => queryClient.invalidateQueries({ queryKey: key }),
	});
	const label = (kind: "person" | "team", id: string) =>
		subjects.find((s) => s.kind === kind && s.id === id)?.label ?? id;
	return (
		<Card>
			<CardHeader>
				<CardTitle className="text-base">Who may use {target}</CardTitle>
				<CardDescription>
					A module page is part of the platform; its roles decide who may use
					it. Grant a role to a team or a person, or revoke one.
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
				{read.data && (
					<>
						<ul aria-label="Role holders" className="space-y-1">
							{read.data.grants.length === 0 && (
								<li className="text-muted-foreground">
									Nobody holds a role on it yet.
								</li>
							)}
							{read.data.grants.map((g) => {
								const who = g.teamId
									? `team ${label("team", g.teamId)}`
									: label("person", g.personId);
								return (
									<li
										key={`${g.role}:${g.teamId}:${g.personId}`}
										className="flex items-center justify-between gap-2"
									>
										<span>
											{g.role}: {who}
										</span>
										<Button
											size="sm"
											variant="outline"
											aria-label={`Revoke ${g.role} from ${who}`}
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
											Revoke
										</Button>
									</li>
								);
							})}
						</ul>
						<SolutionRoleGrantForm
							roles={read.data.roles}
							subjects={subjects}
							allowUnlistedSubjects={false}
							busy={change.isPending}
							onGrant={({ role, personId, teamId }) =>
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
					</>
				)}
			</CardContent>
		</Card>
	);
}
