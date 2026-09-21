"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { OrgSelector } from "@/components/org-selector";
import { orgQueries } from "@/features/organizations/service/queries";
import { useEffectivePermissions } from "@/features/permissions/service/effective";
import { useRoles } from "@/features/roles/service/queries";
import { useAuth } from "@/lib/auth";
import { truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Page as PageBody,
	PageHeader,
	Panel,
	Section,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Stack,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
import {
	groupByResource,
	type Permission,
	permissionLabel,
	rolesGranting,
	sourcesGranting,
} from "../model/effective";
import { permissionQueries } from "../service/queries";
import { GrantSourceBadge, sourceKey } from "./grant-source";

export function PermissionsPage() {
	const { organizationId: orgId = "" } = useAuth();
	return <PermissionsBrowser key={orgId} orgId={orgId} />;
}

function PermissionsBrowser({ orgId }: { orgId: string }) {
	const { data: info, isLoading } = useQuery(permissionQueries.serviceInfo());
	const { data: roles = [] } = useRoles(orgId);

	const declared = info?.capabilities?.permissions ?? [];
	const descriptions = new Map(
		declared.map(
			(permission) =>
				[permissionLabel(permission), permission.description] as const,
		),
	);
	const groups = groupByResource(declared.map(permissionLabel));

	return (
		<PageBody>
			<PageHeader
				title="Permissions"
				description="The vocabulary the service declares, and which roles grant each entry in this organization."
				actions={<OrgSelector />}
			/>

			<CheckAGrant orgId={orgId} />

			{isLoading ? (
				<Panel>
					<span className="text-sm text-muted-foreground">Loading…</span>
				</Panel>
			) : (
				groups.map((group) => (
					<Section key={group.resource} title={group.resource}>
						<Panel>
							<Table>
								<TableHeader>
									<TableRow>
										<TableHead>Permission</TableHead>
										<TableHead>What it allows</TableHead>
										<TableHead>Granted by</TableHead>
									</TableRow>
								</TableHeader>
								<TableBody>
									{group.permissions.map((permission) => {
										const label = permissionLabel(permission);
										const granting = rolesGranting(permission, roles);
										return (
											<TableRow key={label}>
												<TableCell className="font-mono text-xs">
													{label}
												</TableCell>
												<TableCell className="text-sm text-muted-foreground">
													{descriptions.get(label) || "—"}
												</TableCell>
												<TableCell>
													{granting.length === 0 ? (
														<span className="text-sm text-muted-foreground">
															No role
														</span>
													) : (
														<Stack
															direction="row"
															gap={2}
															className="flex-wrap"
														>
															{granting.map((role) => (
																<Badge key={role.id} variant="secondary">
																	{role.name}
																</Badge>
															))}
														</Stack>
													)}
												</TableCell>
											</TableRow>
										);
									})}
								</TableBody>
							</Table>
						</Panel>
					</Section>
				))
			)}
		</PageBody>
	);
}

// "Does this person hold this permission, and by which path" — answered from
// the organization's own role assignments, the same rows the authorization
// service reads. It is a reading of the grants, not a decision by the policy
// decision point: the service's own check is an internal RPC the browser
// cannot reach, so this deliberately says what it is.
function CheckAGrant({ orgId }: { orgId: string }) {
	const [subjectId, setSubjectId] = useState("");
	const [permission, setPermission] = useState("");
	const { data: members } = useQuery(orgQueries.members(orgId));
	const { data: info } = useQuery(permissionQueries.serviceInfo());
	const { permissions } = useEffectivePermissions(orgId, subjectId);

	const wanted: Permission | undefined = (() => {
		const separator = permission.indexOf(":");
		if (separator <= 0) return undefined;
		return {
			resource: permission.slice(0, separator),
			action: permission.slice(separator + 1),
		};
	})();
	const sources = wanted ? sourcesGranting(wanted, permissions) : [];

	return (
		<Section
			title="Check a grant"
			description="Resolved from this organization's role assignments, not from a decision by the authorization service."
		>
			<Panel>
				<Stack gap={4}>
					<Stack direction="row" gap={4} className="flex-wrap">
						<Select
							value={subjectId}
							onValueChange={(value) => setSubjectId(value ?? "")}
						>
							<SelectTrigger className="w-72">
								<SelectValue placeholder="Pick a member…" />
							</SelectTrigger>
							<SelectContent>
								{(members?.members ?? []).map((member) => (
									<SelectItem key={member.userId} value={member.userId}>
										{truncateUUID(member.userId)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Select
							value={permission}
							onValueChange={(value) => setPermission(value ?? "")}
						>
							<SelectTrigger className="w-72">
								<SelectValue placeholder="Pick a permission…" />
							</SelectTrigger>
							<SelectContent>
								{(info?.capabilities?.permissions ?? []).map((entry) => (
									<SelectItem
										key={permissionLabel(entry)}
										value={permissionLabel(entry)}
									>
										{permissionLabel(entry)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Stack>
					{subjectId && wanted && (
						<Stack direction="row" gap={2} align="center" className="flex-wrap">
							{sources.length === 0 ? (
								<Badge variant="outline">Not granted</Badge>
							) : (
								<>
									<Badge variant="secondary">Granted</Badge>
									{sources.map((source) => (
										<GrantSourceBadge key={sourceKey(source)} source={source} />
									))}
								</>
							)}
						</Stack>
					)}
				</Stack>
			</Panel>
		</Section>
	);
}
