"use client";

import Link from "next/link";
import {
	useAuthorityInstallations,
	useRoleGrants,
} from "@/features/solution-installations/service/queries";
import { useTargetTitles } from "@/features/solution-installations/ui/target-name";
import { Installation_Status } from "@/gen/host/installation/v1/installation_pb";
import { Badge, Panel, Section, Spinner, Stack } from "@/shared/ui";

export interface TeamInstallationRole {
	installationId: string;
	target: string;
	role: string;
}

/**
 * The roles a team holds on the organization's installations, from each
 * installation's role grants. Pure, so the page and its test read one answer.
 */
export function teamInstallationRoles(
	teamId: string,
	installations: ReadonlyMap<string, { target: string; status: number }>,
	grants: ReadonlyMap<string, readonly { role: string; teamId: string }[]>,
): TeamInstallationRole[] {
	const held: TeamInstallationRole[] = [];
	for (const [installationId, installation] of installations) {
		if (installation.status === Installation_Status.REVOKED) continue;
		for (const grant of grants.get(installationId) ?? []) {
			if (grant.teamId !== teamId) continue;
			held.push({
				installationId,
				target: installation.target,
				role: grant.role,
			});
		}
	}
	return held.sort(
		(a, b) => a.target.localeCompare(b.target) || a.role.localeCompare(b.role),
	);
}

// What a team holds on solutions and module pages. Those roles are given on
// the installation (Admin → Solutions), not as organization roles, so the
// team's "Roles" section alone said "This team carries no roles" for a team
// the solution page listed as its editors.
export function TeamInstallationAccess({
	teamId,
	orgId,
}: {
	teamId: string;
	orgId: string;
}) {
	const installations = useAuthorityInstallations(orgId);
	const ids = [...(installations.data?.keys() ?? [])];
	const { grants, failed, pending } = useRoleGrants(orgId, ids);
	const titles = useTargetTitles();
	const held = teamInstallationRoles(
		teamId,
		new Map(
			[...(installations.data ?? new Map()).entries()].map(([id, inst]) => [
				id,
				{ target: inst.target, status: inst.status },
			]),
		),
		grants,
	);

	return (
		<Section
			title="Access through solutions and module pages"
			description="Roles given to this team on what the organization has installed. Change them under Admin → Solutions."
		>
			<Panel>
				{installations.isLoading || pending ? (
					<Spinner label="Loading what this team can use" size="sm" />
				) : installations.isError ? (
					<span role="alert" className="text-sm text-muted-foreground">
						What this team can use could not be read.
					</span>
				) : held.length === 0 ? (
					<span className="text-sm text-muted-foreground">
						{failed.size > 0
							? "Some installations could not be read; this team holds no role on the others."
							: "This team holds no role on any solution or module page."}
					</span>
				) : (
					<Stack direction="row" gap={2} className="flex-wrap">
						{held.map((entry) => (
							<Badge
								key={`${entry.installationId}:${entry.role}`}
								variant="secondary"
							>
								<Link
									href={`/admin/solutions/${entry.installationId}`}
									className="hover:underline"
								>
									{titles.get(entry.target) ?? entry.target}: {entry.role}
								</Link>
							</Badge>
						))}
					</Stack>
				)}
			</Panel>
		</Section>
	);
}
