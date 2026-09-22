"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { OrgSelector } from "@/components/org-selector";
import { orgQueries } from "@/features/organizations/service/queries";
import { useEffectivePermissions } from "@/features/permissions/service/effective";
import { useExplainPermission } from "@/features/permissions/service/explain";
import { useRoles } from "@/features/roles/service/queries";
import { useAuth } from "@/lib/auth";
import {
	Badge,
	Input,
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
	type GrantSource,
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

// "Does this person hold this permission, and by which path" — the verdict
// asked of the authorization service itself (ExplainPermission, the
// administrative companion to the internal CheckPermission), and the paths
// resolved from the organization's assignments beside it.
//
// The two answer different questions and are shown as two things. The service
// decides; it does not return a path, so it cannot say what to revoke. The
// local resolution names the role and the team, but reads only assignments
// scoped to this organization, so a globally assigned role is invisible to it.
// Presenting either alone misleads: a verdict with nothing to act on, or a
// path list that quietly omits a grant.
function CheckAGrant({ orgId }: { orgId: string }) {
	const [subjectId, setSubjectId] = useState("");
	const [permission, setPermission] = useState("");
	// The scope being typed and the scope being asked about are two different
	// things. Querying the first would put a decision for "pro" on screen on
	// the way to "project-42" — an authoritative denial for a scope nobody
	// asked about, from a control whose whole purpose is to be believed.
	const [scopeDraft, setScopeDraft] = useState("");
	const [scope, setScope] = useState("");
	const commitScope = () => setScope(scopeDraft.trim());
	// A pasted scope carries whitespace the service can never match, so the
	// question is asked trimmed — and the draft is compared trimmed too, or
	// trailing whitespace alone would read as an unasked question forever.
	const pending = scopeDraft.trim() !== scope;
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
	// Scoped by the same scope the question carries: a path granted only in
	// another scope does not explain this verdict, and rendering it beside one
	// is how a denial acquires a list of reasons it was granted.
	const sources = wanted ? sourcesGranting(wanted, permissions, scope) : [];
	const decision = useExplainPermission(orgId, subjectId, wanted, scope);

	return (
		<Section
			title="Check a grant"
			description="The authorization service's own decision, with the assignments in this organization that explain it."
		>
			<Panel>
				<Stack gap={4}>
					<Stack direction="row" gap={4} className="flex-wrap">
						<Select
							value={subjectId}
							items={(members?.members ?? []).map((member) => ({
								value: member.userId,
								label: member.userEmail || "User unavailable",
							}))}
							onValueChange={(value) => setSubjectId(value ?? "")}
						>
							<SelectTrigger className="w-72">
								<SelectValue placeholder="Pick a member…" />
							</SelectTrigger>
							<SelectContent>
								{(members?.members ?? []).map((member) => (
									<SelectItem key={member.userId} value={member.userId}>
										{member.userEmail || "User unavailable"}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Select
							value={permission}
							items={(info?.capabilities?.permissions ?? []).map((entry) => ({
								value: permissionLabel(entry),
								label: permissionLabel(entry),
							}))}
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
						<Input
							className="w-72"
							value={scopeDraft}
							onChange={(event) => setScopeDraft(event.target.value)}
							onBlur={commitScope}
							onKeyDown={(event) => {
								if (event.key === "Enter") commitScope();
							}}
							placeholder="Scope (empty asks organization-wide)"
							aria-label="Scope"
						/>
					</Stack>
					{subjectId &&
						wanted &&
						(pending ? (
							// The box and the verdict would otherwise disagree: one reading
							// the scope being typed, the other answering the last one asked.
							<span className="text-sm text-muted-foreground">
								Press Enter to ask about{" "}
								{scopeDraft.trim() || "the whole organization"}.
							</span>
						) : (
							<Verdict decision={decision} scope={scope} sources={sources} />
						))}
				</Stack>
			</Panel>
		</Section>
	);
}

function Verdict({
	decision,
	scope,
	sources,
}: {
	decision: ReturnType<typeof useExplainPermission>;
	scope: string;
	sources: readonly GrantSource[];
}) {
	if (decision.isPending) {
		return <span className="text-sm text-muted-foreground">Asking…</span>;
	}
	// The service refuses a subject it will not answer about — one outside this
	// organization, most of all. Rendering that as "not granted" would turn a
	// refusal to answer into an answer.
	if (decision.isError || !decision.data) {
		return (
			<span className="text-sm text-destructive">
				The authorization service did not answer:{" "}
				{decision.error?.message ?? "unknown error"}
			</span>
		);
	}

	const { allowed, reason, grantingScopes } = decision.data;
	// Asked org-wide, the scoped assignments are additional reach. Asked at a
	// scope, a denial beside them is the case the contract warns about: the
	// subject is entitled, elsewhere.
	const elsewhere = grantingScopes.filter((granted) => granted !== scope);

	return (
		<Stack gap={2}>
			<Stack direction="row" gap={2} align="center" className="flex-wrap">
				<Badge variant={allowed ? "secondary" : "outline"}>
					{allowed ? "Allowed" : "Not allowed"}
					{scope ? ` in ${scope}` : " organization-wide"}
				</Badge>
				{reason && (
					<span className="text-sm text-muted-foreground">{reason}</span>
				)}
			</Stack>

			{elsewhere.length > 0 && (
				<Stack direction="row" gap={2} align="center" className="flex-wrap">
					<span className="text-sm text-muted-foreground">
						{allowed ? "Also granted in" : "Granted, but only in"}
					</span>
					{elsewhere.map((granted) => (
						<Badge key={granted} variant="outline">
							{granted}
						</Badge>
					))}
				</Stack>
			)}

			<Stack direction="row" gap={2} align="center" className="flex-wrap">
				{sources.length > 0 ? (
					<>
						{/* Only an allowed verdict is explained by these paths. Scope
						    filtering keeps a grant from another scope out, so a path
						    beside a denial means the assignments moved between the two
						    reads — reported as the disagreement it is, never as a
						    reason the denial happened. */}
						<span className="text-sm text-muted-foreground">
							{allowed
								? "Through"
								: "The service denies this despite these assignments, which may have just changed:"}
						</span>
						{sources.map((source) => (
							<GrantSourceBadge key={sourceKey(source)} source={source} />
						))}
					</>
				) : (
					allowed && (
						<span className="text-sm text-muted-foreground">
							No assignment in this organization explains this — a role assigned
							globally grants it, and revoking it is not an action this
							organization can take.
						</span>
					)
				)}
			</Stack>
		</Stack>
	);
}
