import { activityLabel } from "../model/activity-label";

("use client");

import { useQuery } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { UserPicker } from "@/components/user-picker";
import { useEffectivePermissions } from "@/features/permissions/service/effective";
import { useExplainPermission } from "@/features/permissions/service/explain";
import { useRoles } from "@/features/roles/service/queries";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
import {
	Badge,
	ErrorState,
	Field,
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
	Spinner,
	Stack,
	useLoadingPhase,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
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
	const {
		data: info,
		isLoading,
		isError: infoFailed,
		error: infoError,
	} = useQuery(permissionQueries.serviceInfo());
	const {
		data: roles = [],
		isLoading: rolesLoading,
		isError: rolesFailed,
		error: rolesError,
	} = useRoles(orgId);
	const { indicator, quiet } = useLoadingPhase(isLoading);
	// Which roles grant a permission is the whole answer this page exists to give.
	// A refused or failed role list defaults to [] exactly as an ungranted
	// permission does, so without this every row would read "No role" — a
	// confident, wrong answer on the surface an administrator uses to audit
	// grants. The vocabulary read is separate: lose it and there are no rows to
	// put an answer in, which is why it is reported instead of left blank.
	const rolesOutcome = readOutcome(rolesFailed, rolesError);
	const infoOutcome = readOutcome(infoFailed, infoError);

	const declared = info?.capabilities?.permissions ?? [];
	const descriptions = new Map(
		declared.map(
			(permission) =>
				[
					permissionLabel(permission),
					activityLabel(permission) ?? "Description unavailable",
				] as const,
		),
	);
	const groups = groupByResource(declared.map(permissionLabel));

	return (
		<PageBody>
			<PageHeader
				title="Permissions"
				description="See what people can do and which roles provide access."
				actions={<OrgSelector />}
			/>

			<CheckAGrant orgId={orgId} />

			{quiet ? null : indicator ? (
				<Panel>
					<Spinner label="Loading the declared permissions" size="sm" />
				</Panel>
			) : infoFailed ? (
				<Panel>
					<span role="alert" className="text-sm text-muted-foreground">
						{readOutcomeMessage(
							infoOutcome,
							"the permissions this service declares",
							"",
						)}
					</span>
				</Panel>
			) : groups.length === 0 ? (
				<EmptyState
					icon={ShieldCheck}
					title="No access rules available"
					description="Access rules appear here when applications provide them."
				/>
			) : (
				groups.map((group) => (
					<Section
						key={group.resource}
						title={
							(
								{
									"*": "All areas",
									api_keys: "API keys",
									orgs: "Organizations",
									datasource: "Data sources",
									module_envelope: "Module access limits",
									entitlements: "Plan limits",
									knowledge: "Shared content",
								} as Record<string, string>
							)[group.resource] ??
							group.resource
								.replace(/[-_]/g, " ")
								.replace(/^./, (c) => c.toUpperCase())
						}
					>
						<PermissionTable
							permissions={group.permissions}
							descriptions={descriptions}
							roles={roles}
							rolesLoading={rolesLoading}
							rolesOutcome={rolesOutcome}
						/>
					</Section>
				))
			)}
		</PageBody>
	);
}

const permissionColumn = createColumnHelper<Permission>();

function PermissionTable({
	permissions,
	descriptions,
	roles,
	rolesLoading,
	rolesOutcome,
}: {
	permissions: readonly Permission[];
	descriptions: Map<string, string>;
	roles: Parameters<typeof rolesGranting>[1];
	rolesLoading: boolean;
	rolesOutcome: ReturnType<typeof readOutcome>;
}) {
	const columns = useMemo(
		() => [
			permissionColumn.display({
				id: "activity",
				header: "Activity",
				cell: ({ row }) =>
					descriptions.get(permissionLabel(row.original)) ||
					"Description unavailable",
			}),
			permissionColumn.display({
				id: "access",
				header: "Roles with access",
				cell: ({ row }) => {
					if (rolesLoading) return <Spinner label="Loading roles" size="sm" />;
					if (rolesOutcome !== "empty") {
						// A failed read cannot establish that no role grants access.
						if (rolesOutcome === "forbidden")
							return <span>Not visible to you</span>;
						if (rolesOutcome === "failed") return <span>Unknown</span>;
					}
					const granting = rolesGranting(row.original, roles);
					return granting.length ? (
						<Stack direction="row" gap={2} className="flex-wrap">
							{granting.map((role) => (
								<Badge key={role.id} variant="secondary">
									{role.name}
								</Badge>
							))}
						</Stack>
					) : (
						<span className="text-muted-foreground">No role</span>
					);
				},
			}),
		],
		[descriptions, roles, rolesLoading, rolesOutcome],
	);
	const rows = useMemo(() => [...permissions], [permissions]);
	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});
	return <DataTable table={table} />;
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
	const { data: info } = useQuery(permissionQueries.serviceInfo());
	const {
		permissions,
		isError: sourcesFailed,
		isLoading: sourcesLoading,
	} = useEffectivePermissions(orgId, subjectId);

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
			title="Check someone’s access"
			description="Choose a person and an activity to check their current access."
		>
			<Panel>
				<Stack gap={4}>
					<div className="grid gap-4 lg:grid-cols-3">
						{/* The same picker every other surface chooses a person with: a
						    scroll list of every member is unusable past a few dozen, and
						    two ways to pick a user is one too many. */}
						<UserPicker
							orgId={orgId}
							value={subjectId}
							onChange={setSubjectId}
						/>
						<Field label="Activity">
							{(control) => (
								<Select
									value={permission}
									items={(info?.capabilities?.permissions ?? []).map(
										(entry) => ({
											value: permissionLabel(entry),
											label: activityLabel(entry) || "Description unavailable",
										}),
									)}
									onValueChange={(value) => setPermission(value ?? "")}
								>
									<SelectTrigger {...control} className="w-full">
										<SelectValue placeholder="Choose an activity…" />
									</SelectTrigger>
									<SelectContent>
										{(info?.capabilities?.permissions ?? []).map((entry) => (
											<SelectItem
												key={permissionLabel(entry)}
												value={permissionLabel(entry)}
											>
												{activityLabel(entry) || "Description unavailable"}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							)}
						</Field>
						<Field
							label="Limit to (optional)"
							description="Leave blank to check access across the organization."
						>
							{(control) => (
								<Input
									{...control}
									className="w-full"
									value={scopeDraft}
									onChange={(event) => setScopeDraft(event.target.value)}
									onBlur={commitScope}
									onKeyDown={(event) => {
										if (event.key === "Enter") commitScope();
									}}
									placeholder="Resource identifier"
								/>
							)}
						</Field>
					</div>
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
							<Verdict
								decision={decision}
								scope={scope}
								sources={sources}
								sourcesUnavailable={sourcesFailed || sourcesLoading}
							/>
						))}
				</Stack>
			</Panel>
		</Section>
	);
}

function Verdict({
	sourcesUnavailable,
	decision,
	scope,
	sources,
}: {
	sourcesUnavailable: boolean;
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
			<ErrorState
				title="Couldn't check access"
				detail={failureText("check this person's access", decision.error)}
			/>
		);
	}

	const { allowed, grantingScopes } = decision.data;
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
				{sourcesUnavailable ? (
					<p className="text-sm text-muted-foreground">
						The roles behind this access could not be verified yet.
					</p>
				) : sources.length > 0 ? (
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
