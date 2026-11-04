"use client";

import {
	HostInstallationStatus,
	type OfferRole,
	type OfferScope,
	type OfferTool,
	type ReviewedOffer,
	type RoleGrant,
} from "@codefly-dev/saas-sdk";
import {
	Badge,
	Banner,
	Button,
	Card,
	Disclosure,
	Field,
	Input,
	List,
	ListItem,
	Spinner,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import type { Transport } from "@connectrpc/connect";
import { Fragment, type ReactNode, useMemo, useState } from "react";
import type { SolutionRequestBinding } from "../binding.js";
import { solutionTransport } from "../transport.js";
import {
	accessLevel,
	accessMix,
	byAccess,
	holderName,
	installed,
	isDenied,
	nameList,
	refusalOf,
	type SetupCondition,
	type SetupSubject,
	scopeText,
	statusWords,
	subjectKey,
} from "./model.js";
import { type SetupClients, useSolutionSetup } from "./use-solution-setup.js";

/**
 * Where the setup's calls go: a Connect transport that reaches the host
 * installation interface (`host.installation.v1` InstallationService and
 * OfferService) as the signed-in person, or a solution binding whose `apiBase`
 * does. The component never
 * learns which host it is on.
 */
export type SolutionSetupTransport =
	| { transport: Transport; binding?: never }
	| { binding: SolutionRequestBinding; transport?: never };

export type SolutionSetupProps = SolutionSetupTransport & {
	/**
	 * The organization the signed-in person's session is at, as the host's own
	 * session names it. Every request names it; the authority decides whether
	 * this person may read or change anything there.
	 */
	organizationId: string;
	/** The solution, by its name in the composition (the core's target). */
	target: string;
	/** Conditions the host knows and the authority does not; see {@link SetupCondition}. */
	conditions?: readonly SetupCondition[];
	/** People and teams the host can name, for granting and naming holders. */
	subjects?: readonly SetupSubject[];
	/** Host-owned choices, using this same current offer and grant client. */
	renderAccessChoices?: (
		offer: ReviewedOffer,
		clients: SetupClients,
		reload: () => void,
	) => ReactNode;
	className?: string;
};

/**
 * A solution's setup: is it ready, what each of its roles grants (scopes →
 * module operations → tools, and the permission ceiling), and who holds them.
 * Every fact is the authority's answer through the host installation
 * interface; every refusal is shown with the authority's reason. The page
 * decides nothing: who may see or change what is the authority's to say.
 */
export function SolutionSetup(props: SolutionSetupProps) {
	const { binding, transport: given } = props;
	const transport = useMemo(
		() => given ?? solutionTransport(binding as SolutionRequestBinding),
		[given, binding],
	);
	const { state, clients, reload } = useSolutionSetup(
		transport,
		props.organizationId,
		props.target,
	);
	const { indicator, quiet } = useLoadingPhase(state.phase === "loading");

	return (
		<section
			aria-label={`Setup of ${props.target}`}
			className={["flex flex-col gap-4", props.className]
				.filter(Boolean)
				.join(" ")}
		>
			{indicator ? (
				<Spinner label={`Loading the setup of ${props.target}`} />
			) : quiet || state.phase === "loading" ? null : (
				<>
					<ReadySection
						target={props.target}
						offer={state.offer}
						offerError={state.offerError}
						conditions={props.conditions}
					/>
					{state.offer &&
						props.renderAccessChoices?.(state.offer, clients, reload)}
					<RolesSection offer={state.offer} offerError={state.offerError} />
					<HoldersSection
						organizationId={props.organizationId}
						offer={state.offer}
						offerError={state.offerError}
						grants={state.grants}
						grantsError={state.grantsError}
						subjects={props.subjects}
						clients={clients}
						onChanged={reload}
					/>
				</>
			)}
		</section>
	);
}

/** What the authority said instead of an answer, with its reason. */
function Refusal({ error, what }: { error: unknown; what: string }) {
	return isDenied(error) ? (
		<Banner tone="info" title={`The authority does not show you ${what}`}>
			{refusalOf(error)}
		</Banner>
	) : (
		<Banner tone="danger" title={`${what} could not be read`}>
			{refusalOf(error)}
		</Banner>
	);
}

function Fact({ term, children }: { term: string; children: ReactNode }) {
	return (
		<div>
			<dt className="type-caption text-muted-foreground">{term}</dt>
			<dd className="mt-1">{children}</dd>
		</div>
	);
}

function ReadySection({
	target,
	offer,
	offerError,
	conditions,
}: {
	target: string;
	offer?: ReviewedOffer;
	offerError?: unknown;
	conditions?: readonly SetupCondition[];
}) {
	const installation = offer?.installation;
	const status = installation?.status;
	return (
		<Card
			title="Is it ready?"
			description="Whether this solution is installed at your organization, at which release, and what it still needs from a person."
		>
			<div className="flex flex-col gap-3">
				{offer ? (
					<dl className="grid gap-3 sm:grid-cols-4">
						<Fact term="Installation">
							<Badge
								tone={
									status === HostInstallationStatus.ACTIVE
										? "success"
										: status === HostInstallationStatus.PENDING
											? "warning"
											: "neutral"
								}
								dot
							>
								{status === undefined ? "Not installed" : statusWords[status]}
							</Badge>
						</Fact>
						<Fact term="Release generation">
							{offer.approval ? String(offer.approval.generation) : "—"}
						</Fact>
						<Fact term="Installation revision">
							{installation ? String(installation.revision) : "—"}
						</Fact>
						<Fact term="Agent role">{installation?.role || "—"}</Fact>
					</dl>
				) : (
					<Refusal error={offerError} what={`the setup of ${target}`} />
				)}
				{offer && !installed(offer) && (
					<p>
						Not installed at this organization. An administrator installs it
						where this host installs solutions, consenting to the roles and the
						permission ceiling below; nobody holds a role until then.
					</p>
				)}
				{conditions && conditions.length > 0 && (
					<ul className="flex flex-col gap-3" aria-label="Unmet conditions">
						{conditions.map((condition) => (
							<ConditionItem key={condition.key} condition={condition} />
						))}
					</ul>
				)}
			</div>
		</Card>
	);
}

function ConditionItem({ condition }: { condition: SetupCondition }) {
	const [busy, setBusy] = useState(false);
	const [refusal, setRefusal] = useState<string | null>(null);
	const action = condition.action;
	return (
		<li className="rounded-lg border p-3">
			<p className="type-emphasis">
				{condition.heading}
				{condition.subject && (
					<span className="ml-1 font-mono type-caption text-muted-foreground">
						{condition.subject}
					</span>
				)}
			</p>
			<p className="mt-1 text-muted-foreground">{condition.detail}</p>
			{condition.clearedBy && (
				<p className="mt-2 type-caption text-muted-foreground">
					Cleared by: {condition.clearedBy}
				</p>
			)}
			{action?.kind === "link" && (
				<a className="mt-2 inline-block underline" href={action.href}>
					{action.label}
				</a>
			)}
			{action?.kind === "note" && (
				<p className="mt-2 italic text-muted-foreground">{action.label}</p>
			)}
			{action?.kind === "perform" && (
				<Button
					className="mt-2"
					size="sm"
					variant="outline"
					disabled={busy}
					onClick={() => {
						setBusy(true);
						setRefusal(null);
						action
							.onPerform()
							.catch((error: unknown) => setRefusal(refusalOf(error)))
							.finally(() => setBusy(false));
					}}
				>
					{action.label}
				</Button>
			)}
			{refusal && (
				<Banner className="mt-2" tone="danger" title="Refused">
					{refusal}
				</Banner>
			)}
		</li>
	);
}

/** The scopes a role or binding names, each as a sentence. */
function Scopes({
	scopes,
	confined,
}: {
	scopes: readonly OfferScope[];
	confined: boolean;
}) {
	return (
		<span className="inline-flex flex-wrap gap-1.5">
			{scopes.map((s) => (
				<Badge key={s.resourceKind + s.actions.join()} variant="secondary">
					{scopeText(s, confined)}
				</Badge>
			))}
		</span>
	);
}

/**
 * One grouped view of what every role reaches, in place of one full table per
 * role.
 *
 * The three tables this replaced were near-copies: the operations a solution
 * composes are the same set, and a role differs from its neighbour by a handful
 * of scopes. Three copies of fifty rows hid that difference in 150 rows; one
 * row per operation with a column per role shows it.
 *
 * `operation` is a wire path, which is a developer's artefact and not what an
 * administrator came to read, so operations are grouped by their module and each
 * group is closed until asked for, with its count and access mix on the line
 * that opens it.
 *
 * Inside a module the rows are banded by the access they demand, because the
 * question a reader brings is "why has this role so many permissions" and the
 * answer is never the total: forty-one reads and eleven writes are not the same
 * breadth, and a flat list of fifty-two rows says only "fifty-two".
 */
interface ReachRow {
	module: string;
	operation: string;
	requires: readonly OfferScope[];
	tools: readonly OfferTool[];
	roles: Set<string>;
}

/** One access level inside a module, with the operations that demand it. */
interface AccessBand {
	level: string;
	rows: ReachRow[];
}

interface ReachGroup {
	module: string;
	rows: ReachRow[];
	bands: AccessBand[];
	/** Roles reaching exactly the same operations here, named together. */
	coinciding: string[][];
}

function reach(roles: readonly OfferRole[]): ReachGroup[] {
	const rows = new Map<string, ReachRow>();
	for (const role of roles) {
		for (const c of role.capabilities) {
			const key = `${c.module}\u0000${c.operation}`;
			const row = rows.get(key) ?? {
				module: c.module,
				operation: c.operation,
				requires: c.requires,
				tools: c.tools,
				roles: new Set<string>(),
			};
			row.roles.add(role.name);
			rows.set(key, row);
		}
	}
	const groups = new Map<string, ReachGroup>();
	for (const row of rows.values()) {
		const group = groups.get(row.module) ?? {
			module: row.module,
			rows: [],
			bands: [],
			coinciding: [],
		};
		group.rows.push(row);
		groups.set(row.module, group);
	}
	for (const group of groups.values()) {
		// Reads and writes apart. "Why has this role so many permissions" is
		// answered by the split, never by the total: forty reads and eleven
		// writes are not the same breadth, and a flat list of fifty-two rows
		// says only "fifty-two".
		const bands = new Map<string, ReachRow[]>();
		for (const row of group.rows) {
			const level = accessLevel(row.requires);
			bands.set(level, [...(bands.get(level) ?? []), row]);
		}
		group.bands = [...bands.entries()]
			.sort(([a], [b]) => byAccess(a, b))
			.map(([level, rows]) => ({ level, rows }));
		// Two roles reaching the same operations in a module is the fact a
		// reader would otherwise have to find by diffing two columns over fifty
		// rows. It is stated instead.
		const signatures = new Map<string, string[]>();
		for (const role of roles) {
			const reached = group.rows
				.filter((row) => row.roles.has(role.name))
				.map((row) => row.operation)
				.sort()
				.join("\u0000");
			if (!reached) continue;
			signatures.set(reached, [...(signatures.get(reached) ?? []), role.name]);
		}
		group.coinciding = [...signatures.values()].filter(
			(names) => names.length > 1,
		);
	}
	return [...groups.values()];
}

function RolesSection({
	offer,
	offerError,
}: {
	offer?: ReviewedOffer;
	offerError?: unknown;
}) {
	const groups = offer ? reach(offer.roles) : [];
	// A column that can only ever repeat one sentence is not a column. When no
	// tool is exposed anywhere, the banner above says so once and the column is
	// gone, rather than printing the same words in every cell.
	const anyTools = groups.some((g) => g.rows.some((r) => r.tools.length > 0));
	return (
		<Card
			title="Roles and what they grant"
			description="What each role grants, limited to what this installation owns, and the module operations that covers. The authority checks them again on every call."
		>
			{!offer ? (
				<Refusal error={offerError} what="the roles" />
			) : (
				<div className="flex flex-col gap-4">
					{!offer.toolsComposed && (
						<Banner
							tone="info"
							title="Tool exposure is unavailable from this host."
						>
							No operation below can be shown as an agent tool, and nothing
							about this installation is wrong.
						</Banner>
					)}
					<div>
						<p className="type-emphasis">Permission ceiling</p>
						<p className="text-muted-foreground">
							What the solution may ask of each module it calls.
						</p>
						{offer.ceiling.length === 0 ? (
							<p>The solution calls no other module.</p>
						) : (
							<List variant="divided" label="Permission ceiling">
								{offer.ceiling.map((c) => (
									<ListItem
										key={c.binding}
										description={
											// A module with no scope under it read as a bare name, which
											// looks like a value still loading. It is a real answer and
											// says so.
											c.scopes.length === 0 ? (
												<span className="text-muted-foreground">
													Reached, but granted no scope of its own.
												</span>
											) : (
												<Scopes scopes={c.scopes} confined={false} />
											)
										}
									>
										{c.module}
									</ListItem>
								))}
							</List>
						)}
					</div>
					{offer.roles.length === 0 ? (
						<p>This solution declares no role.</p>
					) : (
						<>
							<div>
								<p className="type-emphasis">Roles</p>
								<List variant="divided" label="Roles">
									{offer.roles.map((role) => (
										<ListItem
											key={role.name}
											description={<Scopes scopes={role.scopes} confined />}
											// The total alone is what made the installer's breadth
											// unreadable; the split is the answer.
											meta={
												role.capabilities.length === 0
													? "No operation"
													: `${role.capabilities.length} ${
															role.capabilities.length === 1
																? "operation"
																: "operations"
														} · ${accessMix(
															role.capabilities.map((c) => c.requires),
														)}`
											}
										>
											{role.name}
										</ListItem>
									))}
								</List>
							</div>
							<div className="flex flex-col gap-2">
								<p className="type-emphasis">What the roles reach</p>
								{groups.length === 0 ? (
									<p>No composed operation is covered by any role.</p>
								) : (
									groups.map((group) => (
										<Disclosure
											key={group.module}
											headingLevel={4}
											// "requires documents: read" understated a module whose
											// fifty-two operations include eleven writes. The line
											// names every access level in it, counted.
											title={
												<span>
													{`${group.module} · ${group.rows.length} ${
														group.rows.length === 1 ? "operation" : "operations"
													} · ${accessMix(
														group.rows.map((row) => row.requires),
													)}`}
													{/* On the closed line, because the point of saying
													    it is that the reader does not have to open the
													    module and diff two columns over fifty rows. */}
													{group.coinciding.map((names) => (
														<span
															key={names.join()}
															className="block type-card-description text-muted-foreground"
														>
															{`${nameList(names)} reach exactly the same operations here.`}
														</span>
													))}
												</span>
											}
										>
											<ReachTable
												group={group}
												roles={offer.roles}
												showTools={anyTools}
											/>
										</Disclosure>
									))
								)}
							</div>
						</>
					)}
				</div>
			)}
		</Card>
	);
}

function ReachTable({
	group,
	roles,
	showTools,
}: {
	group: ReachGroup;
	roles: readonly OfferRole[];
	showTools: boolean;
}) {
	// The expander's columns: operation, requires, one per role, and tools.
	const columns = 2 + roles.length + (showTools ? 1 : 0);
	return (
		<Table aria-label={`Operations of ${group.module}`}>
			<TableHeader>
				<TableRow>
					<TableHead>Operation</TableHead>
					<TableHead>Requires</TableHead>
					{roles.map((role) => (
						<TableHead key={role.name}>{role.name}</TableHead>
					))}
					{showTools && <TableHead>Agent tools</TableHead>}
				</TableRow>
			</TableHeader>
			<TableBody>
				{group.bands.map((band) => (
					<Fragment key={band.level}>
						<TableRow>
							<TableHead colSpan={columns} scope="rowgroup">
								{band.level} · {band.rows.length}{" "}
								{band.rows.length === 1 ? "operation" : "operations"}
							</TableHead>
						</TableRow>
						{band.rows.map((row) => (
							<TableRow key={row.operation}>
								<TableCell className="whitespace-normal break-words">
									<code>{row.operation}</code>
								</TableCell>
								<TableCell className="whitespace-normal">
									<Scopes scopes={row.requires} confined={false} />
								</TableCell>
								{roles.map((role) => (
									<TableCell key={role.name}>
										{row.roles.has(role.name) ? "Yes" : "—"}
									</TableCell>
								))}
								{showTools && (
									<TableCell className="whitespace-normal">
										{row.tools.length === 0 ? (
											"Not exposed as a tool"
										) : (
											<span className="inline-flex flex-wrap gap-1.5">
												{row.tools.map((t) => (
													<Badge
														key={t.binding}
														tone={t.write ? "warning" : "info"}
														title={t.binding}
													>
														{t.name}
														{t.write ? " · changes data" : ""}
													</Badge>
												))}
											</span>
										)}
									</TableCell>
								)}
							</TableRow>
						))}
					</Fragment>
				))}
			</TableBody>
		</Table>
	);
}

function HoldersSection({
	organizationId,
	offer,
	offerError,
	grants,
	grantsError,
	subjects,
	clients,
	onChanged,
}: {
	organizationId: string;
	offer?: ReviewedOffer;
	offerError?: unknown;
	grants?: RoleGrant[];
	grantsError?: unknown;
	subjects?: readonly SetupSubject[];
	clients: SetupClients;
	onChanged: () => void;
}) {
	const [busy, setBusy] = useState(false);
	const [refusal, setRefusal] = useState<string | null>(null);
	const installation = offer?.installation;
	const change = (what: Promise<unknown>) => {
		setBusy(true);
		setRefusal(null);
		what
			.then(onChanged)
			.catch((error: unknown) => setRefusal(refusalOf(error)))
			.finally(() => setBusy(false));
	};
	const request = (grant: {
		role: string;
		personId: string;
		teamId: string;
	}) => ({
		organizationId,
		installationId: installation?.id ?? "",
		...grant,
	});
	return (
		<Card
			title="Who holds them"
			description="The people and teams holding a role on this installation. A role granted here gives its holder the operations and tools above, on this installation only."
		>
			<div className="flex flex-col gap-3">
				{!offer ? (
					<Refusal error={offerError} what="who holds the roles" />
				) : !installed(offer) || !installation ? (
					<p>Nobody holds a role until the solution is installed.</p>
				) : grantsError !== undefined ? (
					<Refusal error={grantsError} what="who holds the roles" />
				) : (
					<>
						{refusal && (
							<Banner
								tone="danger"
								title="The authority refused the change"
								onDismiss={() => setRefusal(null)}
							>
								{refusal}
							</Banner>
						)}
						{!grants || grants.length === 0 ? (
							<p>No person or team holds a role on this installation yet.</p>
						) : (
							<List variant="divided" label="Role grants">
								{grants.map((g) => (
									<ListItem
										key={g.role + g.personId + g.teamId}
										actions={
											<Button
												size="sm"
												variant="outline"
												disabled={busy}
												onClick={() =>
													change(clients.installations.revokeRole(request(g)))
												}
											>
												Revoke {g.role} from {holderName(g, subjects)}
											</Button>
										}
									>
										{g.role} · {holderName(g, subjects)}
									</ListItem>
								))}
							</List>
						)}
						{installation.status === HostInstallationStatus.ACTIVE ? (
							<SolutionRoleGrantForm
								roles={offer.roles.map((r) => r.name)}
								subjects={subjects}
								busy={busy}
								onGrant={(grant) =>
									change(clients.installations.grantRole(request(grant)))
								}
							/>
						) : (
							<p className="text-muted-foreground">
								A role can be granted once the installation is active.
							</p>
						)}
					</>
				)}
			</div>
		</Card>
	);
}

export function SolutionRoleGrantForm({
	roles,
	subjects,
	busy,
	onGrant,
	allowUnlistedSubjects = true,
	subjectLabel = "Person or team",
	submitLabel = "Grant role",
	fixedRole,
}: {
	roles: string[];
	subjects?: readonly SetupSubject[];
	busy: boolean;
	onGrant: (grant: { role: string; personId: string; teamId: string }) => void;
	allowUnlistedSubjects?: boolean;
	subjectLabel?: string;
	submitLabel?: string;
	fixedRole?: string;
}) {
	const [selectedRole, setRole] = useState("");
	const role = fixedRole ?? selectedRole;
	const [subject, setSubject] = useState("");
	const [kind, setKind] = useState<"person" | "team">("person");
	const [id, setId] = useState("");
	const named = !allowUnlistedSubjects || Boolean(subjects?.length);
	const chosen = named ? subject : id.trim() ? subjectKey(kind, id.trim()) : "";
	return (
		<form
			className="flex flex-col gap-3"
			onSubmit={(event) => {
				event.preventDefault();
				if (
					busy ||
					!roles.includes(role) ||
					!chosen ||
					(named && !subjects?.some((s) => subjectKey(s.kind, s.id) === chosen))
				)
					return;
				const [k, ...rest] = chosen.split(":");
				const value = rest.join(":");
				onGrant({
					role,
					personId: k === "person" ? value : "",
					teamId: k === "team" ? value : "",
				});
			}}
		>
			{fixedRole ? (
				<p>Role: {fixedRole}</p>
			) : (
				<Field label="Role">
					{(control) => (
						<select
							{...control}
							required
							value={role}
							onChange={(event) => setRole(event.target.value)}
						>
							<option value="" disabled>
								Choose a role…
							</option>
							{roles.map((r) => (
								<option key={r} value={r}>
									{r}
								</option>
							))}
						</select>
					)}
				</Field>
			)}
			{named ? (
				<Field label={subjectLabel}>
					{(control) => (
						<select
							{...control}
							required
							value={subject}
							onChange={(event) => setSubject(event.target.value)}
						>
							<option value="" disabled>
								Choose {subjectLabel.toLowerCase()}…
							</option>
							{subjects?.map((s) => (
								<option
									key={subjectKey(s.kind, s.id)}
									value={subjectKey(s.kind, s.id)}
								>
									{s.kind === "team" ? `Team ${s.label}` : s.label}
								</option>
							))}
						</select>
					)}
				</Field>
			) : (
				<div className="flex flex-wrap gap-3">
					<Field label="Grant to">
						{(control) => (
							<select
								{...control}
								value={kind}
								onChange={(event) =>
									setKind(event.target.value === "team" ? "team" : "person")
								}
							>
								<option value="person">A person</option>
								<option value="team">A team</option>
							</select>
						)}
					</Field>
					<Field label={kind === "team" ? "Team id" : "Person id"}>
						{(control) => (
							<Input
								{...control}
								required
								value={id}
								onChange={(event) => setId(event.target.value)}
							/>
						)}
					</Field>
				</div>
			)}
			<div>
				<Button type="submit" disabled={busy || !role || !chosen}>
					{submitLabel}
				</Button>
			</div>
		</form>
	);
}
