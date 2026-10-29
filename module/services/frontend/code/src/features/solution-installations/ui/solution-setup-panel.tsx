"use client";

import { HostInstallationStatus } from "@codefly-dev/saas-sdk";
import {
	type SetupCondition,
	type SetupSubject,
	SolutionSetup,
} from "@codefly-dev/saas-ui/solution";
import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { teamQueries } from "@/features/teams/service/queries";
import type { Installation } from "@/gen/saas/accounts/v1/installations_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { useOrgMembers } from "@/lib/hooks/use-organizations";
import { Banner } from "@/shared/ui";
import { InstallationTeamChoices } from "./installation-team-choices";

/**
 * This host's mount of the one Setup UI, `<SolutionSetup>`, the same component
 * the local authority and every solution playground render. The host adds
 * only what it knows and the authority does not: the organization the session
 * is at, the people and teams it can name, and its own unmet conditions.
 * Every fact and every refusal on the page is the authority's.
 */
export function SolutionSetupPanel({
	target,
	conditions,
	targetId,
	installation,
}: {
	target: string;
	targetId?: string;
	installation?: Installation;
	conditions?: readonly SetupCondition[];
}) {
	const { organizationId = "" } = useAuth();
	// Names only: a person who may not list them sees ids, and the authority
	// still decides every grant.
	const members = useOrgMembers(organizationId || null);
	const teams = useQuery(teamQueries.list(organizationId));
	const subjects = useMemo<SetupSubject[]>(
		() => [
			...(members.data ?? []).map((m) => ({
				kind: "person" as const,
				id: m.userId,
				label: m.userEmail || m.userId,
			})),
			...(teams.data?.teams ?? []).map((t) => ({
				kind: "team" as const,
				id: t.id,
				label: t.name || t.id,
			})),
		],
		[members.data, teams.data],
	);
	if (!organizationId) {
		return (
			<Banner tone="info" title="Your session is at no organization">
				A solution&apos;s setup is read at the organization your session is at.
				Choose one to see its roles.
			</Banner>
		);
	}
	return (
		<SolutionSetup
			key={`${installation?.id}:${installation?.revision}:${installation?.status}`}
			transport={apiTransport}
			organizationId={organizationId}
			target={target}
			conditions={conditions}
			subjects={subjects}
			renderAccessChoices={
				targetId && installation
					? (offer, clients, reload) => (
							<InstallationTeamChoices
								orgId={organizationId}
								targetId={targetId}
								offer={offer}
								grant={
									offer.installation?.id === installation.id &&
									offer.installation.status === HostInstallationStatus.ACTIVE
										? async (choice) => {
												await clients.installations.grantRole({
													organizationId,
													installationId: installation.id,
													teamId: choice.teamId,
													role: choice.role,
												});
											}
										: undefined
								}
								onApplied={reload}
							/>
						)
					: undefined
			}
		/>
	);
}
