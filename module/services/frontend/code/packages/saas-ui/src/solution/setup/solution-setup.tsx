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
	type AbilityRole,
	article,
	type RoleAbilities,
	roleAbilities,
	unreachedText,
} from "./abilities.js";
import {
	accessLevel,
	accessMix,
	byAccess,
	holderName,
	installed,
	isDenied,
	refusalOf,
	type SetupCondition,
	type SetupFact,
	type SetupSubject,
	scopeText,
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
	/**
	 * What the host knows of the installation beside the authority ("Healthy"),
	 * shown on its status line.
	 */
	facts?: readonly SetupFact[];
	/**
	 * When the authority offers an update: what it asks beyond what was
	 * approved, in words (`updateAsks`), when the host has read it, and the
	 * host's control that reviews it.
	 */
	update?: {
		asks?: readonly RoleAbilities[];
		/**
		 * Each role's reach as the installation's consent approved it: what its
		 * holders hold until the update. The roles are read from it while an
		 * update waits, never from the release not yet approved.
		 */
		approved?: readonly AbilityRole[];
		action?: ReactNode;
	};
	/** The host's own control for a team holding a role: its people, in place. */
	teamControls?: (team: SetupSubject) => ReactNode;
	className?: string;
};

/**
 * A solution's setup, read by an organization administrator: where it stands,
 * what each role lets people do and who has it, and — behind "Technical
 * details" — the identifiers an engineer reads. Every fact is the authority's
 * answer through the host installation interface; every refusal is shown
 * with the authority's reason. The page decides nothing: who may see or change
 * what is the authority's to say.
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
					<StatusSection
						target={props.target}
						offer={state.offer}
						offerError={state.offerError}
						conditions={props.conditions}
						facts={props.facts}
						update={props.update}
					/>
					<RolesSection
						organizationId={props.organizationId}
						offer={state.offer}
						offerError={state.offerError}
						grants={state.grants}
						grantsError={state.grantsError}
						subjects={props.subjects}
						teamControls={props.teamControls}
						approved={
							state.offer?.installation?.upgradeAvailable
								? props.update?.approved
								: undefined
						}
						clients={clients}
						onChanged={reload}
					/>
					{state.offer && <TechnicalDetails offer={state.offer} />}
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

/** One role's abilities, grouped by where: the words every surface uses. */
export function RoleAbilitiesList({
	abilities,
	label,
}: {
	abilities: RoleAbilities;
	label?: string;
}) {
	if (!abilities.groups.length)
		return <p className="text-muted-foreground">Nothing yet.</p>;
	return (
		<ul
			className="flex flex-col gap-2"
			aria-label={
				label ??
				`What ${article(abilities.role).toLowerCase()} ${abilities.role} can do`
			}
		>
			{abilities.groups.map((group) => (
				<li key={group.key}>
					<p className="type-emphasis">{group.where}</p>
					<ul className="list-disc pl-5">
						{group.lines.map((line) => (
							<li key={line}>{capitalized(line)}</li>
						))}
					</ul>
				</li>
			))}
		</ul>
	);
}

const capitalized = (text: string) =>
	text ? text[0].toUpperCase() + text.slice(1) : text;

/** The installation's lifecycle in a reader's words. */
function statusFact(offer: ReviewedOffer): SetupFact {
	switch (offer.installation?.status) {
		case HostInstallationStatus.ACTIVE:
			return { text: "Installed", tone: "success" };
		case HostInstallationStatus.PENDING:
			return { text: "Being set up", tone: "warning" };
		case HostInstallationStatus.REVOKED:
			return { text: "Uninstalled", tone: "neutral" };
		default:
			return { text: "Not installed", tone: "neutral" };
	}
}

function StatusSection({
	target,
	offer,
	offerError,
	conditions,
	facts,
	update,
}: {
	target: string;
	offer?: ReviewedOffer;
	offerError?: unknown;
	conditions?: readonly SetupCondition[];
	facts?: readonly SetupFact[];
	update?: SolutionSetupProps["update"];
}) {
	const unreached = offer?.installation?.unreached ?? [];
	const line: SetupFact[] = offer ? [statusFact(offer)] : [];
	if (offer && installed(offer) && conditions)
		line.push(
			conditions.length
				? {
						text: `${conditions.length} ${conditions.length === 1 ? "thing" : "things"} to fix`,
						tone: "danger",
					}
				: { text: "Ready", tone: "success" },
		);
	if (offer && installed(offer)) line.push(...(facts ?? []));
	return (
		<Card title="Status">
			<div className="flex flex-col gap-3">
				{offer ? (
					<ul className="flex flex-wrap items-center gap-2" aria-label="Status">
						{line.map((fact) => (
							<li key={fact.text}>
								<Badge tone={fact.tone} dot>
									{fact.text}
								</Badge>
							</li>
						))}
					</ul>
				) : (
					<Refusal error={offerError} what={`the setup of ${target}`} />
				)}
				{offer && !installed(offer) && (
					<p>
						Not installed in your organization. An administrator installs it
						from Solutions, after reviewing what each role can do. Nobody can
						use it until then.
					</p>
				)}
				{offer?.installation?.upgradeAvailable && (
					<Banner tone="warning" title="Update available">
						<div className="flex flex-col gap-2">
							{update?.asks?.length ? (
								<>
									<p>
										This release asks for more than you approved. Until you
										update, everyone keeps exactly the access you approved.
									</p>
									{update.asks.map((asks) => (
										<div key={asks.role}>
											<p className="type-emphasis">
												{article(asks.role)} {asks.role} would also be able to
											</p>
											<RoleAbilitiesList
												abilities={asks}
												label={`What the update adds for ${article(asks.role).toLowerCase()} ${asks.role}`}
											/>
										</div>
									))}
								</>
							) : (
								<p>
									A newer release asks for more than you approved. Until you
									review and update it, everyone keeps exactly the access you
									approved.
								</p>
							)}
							{update?.action}
						</div>
					</Banner>
				)}
				{unreached.length > 0 && (
					<Banner tone="warning" title="Some access can't be reached right now">
						<div className="flex flex-col gap-2">
							<ul className="list-disc pl-5" aria-label="Unreached access">
								{unreached.map((condition) => (
									<li key={condition}>{unreachedText(condition)}</li>
								))}
							</ul>
							<p>
								Until a later release of the module it goes through restores it,
								the people holding that role can't do this; everything else they
								hold keeps working, and nothing is ever widened meanwhile.
							</p>
						</div>
					</Banner>
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
 * Each role: what a holder can do, grouped by where, and who has it — the
 * teams and people holding it, each removable, and a control giving it to
 * someone else. A role's holders sit beside what the role lets them do, so
 * a reader never matches a grant list against a role list.
 */
function RolesSection({
	organizationId,
	offer,
	offerError,
	grants,
	grantsError,
	subjects,
	teamControls,
	approved,
	clients,
	onChanged,
}: {
	organizationId: string;
	offer?: ReviewedOffer;
	offerError?: unknown;
	grants?: RoleGrant[];
	grantsError?: unknown;
	subjects?: readonly SetupSubject[];
	teamControls?: (team: SetupSubject) => ReactNode;
	/** The approved reach, while an update waits. */
	approved?: readonly AbilityRole[];
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
	// While an update waits, holders hold what was approved: the roles read
	// that, and say so; a consent that recorded no reach reads the release
	// under review, and says that instead.
	const waiting = offer?.installation?.upgradeAvailable === true;
	const read = (role: OfferRole): AbilityRole =>
		(waiting && approved?.find((a) => a.name === role.name)) || role;
	const abilities = offer
		? roleAbilities(offer.target, offer.roles.map(read))
		: [];
	return (
		<Card
			title="Roles"
			description={
				!waiting
					? "What each role lets people do, and who has it. Nobody can use it without a role."
					: approved?.length
						? "What each role lets people do today, as you approved it, and who has it. The update would add what is listed above."
						: "What each role lets people do once updated, and who has it. Until you update, what the update adds is not available to anyone."
			}
		>
			{!offer ? (
				<Refusal error={offerError} what="the roles" />
			) : offer.roles.length === 0 ? (
				<p>It declares no role.</p>
			) : (
				<div className="flex flex-col gap-4">
					{refusal && (
						<Banner
							tone="danger"
							title="The authority refused the change"
							onDismiss={() => setRefusal(null)}
						>
							{refusal}
						</Banner>
					)}
					{abilities.map((role) => (
						<section
							key={role.role}
							aria-label={`Role ${role.role}`}
							className="flex flex-col gap-3 rounded-lg border p-4"
						>
							<h4 className="type-section-title">
								What {article(role.role).toLowerCase()} {role.role} can do
							</h4>
							<RoleAbilitiesList abilities={role} />
							<div className="flex flex-col gap-2">
								<p className="type-emphasis">Who has it</p>
								{!installed(offer) || !installation ? (
									<p className="text-muted-foreground">
										Nobody, until it is installed.
									</p>
								) : grantsError !== undefined ? (
									<Refusal error={grantsError} what="who has this role" />
								) : (
									<Holders
										role={role.role}
										grants={(grants ?? []).filter((g) => g.role === role.role)}
										subjects={subjects}
										teamControls={teamControls}
										busy={busy}
										onRevoke={(g) =>
											change(clients.installations.revokeRole(request(g)))
										}
									/>
								)}
								{installed(offer) &&
									installation &&
									grantsError === undefined &&
									(installation.status === HostInstallationStatus.ACTIVE ? (
										<SolutionRoleGrantForm
											roles={[role.role]}
											fixedRole={role.role}
											subjects={subjects}
											busy={busy}
											submitLabel={`Give ${role.role} access`}
											onGrant={(grant) =>
												change(clients.installations.grantRole(request(grant)))
											}
										/>
									) : (
										<p className="text-muted-foreground">
											It can be given once the installation is active.
										</p>
									))}
							</div>
						</section>
					))}
				</div>
			)}
		</Card>
	);
}

/** The teams and people holding one role, each removable. */
function Holders({
	role,
	grants,
	subjects,
	teamControls,
	busy,
	onRevoke,
}: {
	role: string;
	grants: readonly RoleGrant[];
	subjects?: readonly SetupSubject[];
	teamControls?: (team: SetupSubject) => ReactNode;
	busy: boolean;
	onRevoke: (grant: RoleGrant) => void;
}) {
	if (!grants.length)
		return <p className="text-muted-foreground">Nobody has it yet.</p>;
	return (
		<ul className="flex flex-col gap-2" aria-label={`Who has ${role}`}>
			{grants.map((g) => {
				const kind = g.teamId ? "team" : "person";
				const subject = subjects?.find(
					(s) => s.kind === kind && s.id === (g.teamId || g.personId),
				) ?? {
					kind,
					id: g.teamId || g.personId,
					label: g.teamId || g.personId,
				};
				const name = holderName(g, subjects);
				return (
					<li
						key={g.role + g.personId + g.teamId}
						className="flex flex-col gap-2 rounded-md bg-muted/40 p-2"
					>
						<div className="flex flex-wrap items-center justify-between gap-2">
							<span>
								{subject.href ? (
									<a className="underline" href={subject.href}>
										{name}
									</a>
								) : (
									name
								)}
								{subject.detail && (
									<span className="text-muted-foreground">
										{" "}
										· {subject.detail}
									</span>
								)}
							</span>
							<Button
								size="sm"
								variant="outline"
								disabled={busy}
								aria-label={`Remove ${role} from ${name}`}
								onClick={() => onRevoke(g)}
							>
								Remove
							</Button>
						</div>
						{kind === "team" && teamControls?.(subject)}
					</li>
				);
			})}
		</ul>
	);
}

/**
 * What an engineer reads, closed until asked for: the consent's identifiers,
 * the modules the solution may call as itself (the permission ceiling), each
 * role's reach with its path and operations, and the operations each role
 * covers, module by module.
 */
function TechnicalDetails({ offer }: { offer: ReviewedOffer }) {
	const groups = reach(offer.roles);
	const ceiling = ceilingEntries(offer);
	const anyTools = groups.some((g) => g.rows.some((r) => r.tools.length > 0));
	const installation = offer.installation;
	return (
		<Disclosure title="Technical details" headingLevel={3}>
			<div className="flex flex-col gap-4">
				<dl className="grid gap-3 sm:grid-cols-3">
					<Fact term="Release generation">
						{offer.approval ? String(offer.approval.generation) : "—"}
					</Fact>
					<Fact term="Installation revision">
						{installation ? String(installation.revision) : "—"}
					</Fact>
					<Fact term="Agent role">{installation?.role || "—"}</Fact>
				</dl>
				{(installation?.unreached.length ?? 0) > 0 && (
					<div>
						<p className="type-emphasis">
							Unreached, as the authority states it
						</p>
						<ul className="list-disc pl-5 font-mono type-caption">
							{installation?.unreached.map((u) => (
								<li key={u}>{u}</li>
							))}
						</ul>
					</div>
				)}
				<div>
					<p className="type-emphasis">Permission ceiling</p>
					<p className="text-muted-foreground">
						Each module the solution calls as itself, with the most it may ask
						there, and each module its declaration depends on, which it reaches
						as the person using it.
					</p>
					{ceiling.length === 0 ? (
						<p>It binds no module and depends on none.</p>
					) : (
						<List variant="divided" label="Permission ceiling">
							{ceiling.map((c) => (
								<ListItem
									key={c.key}
									description={
										<span className="flex flex-col gap-1">
											{c.scopes.length > 0 ? (
												<Scopes scopes={c.scopes} confined={false} />
											) : c.presence === undefined ? (
												<span className="text-muted-foreground">
													Reached, but granted no scope of its own.
												</span>
											) : (
												<span className="text-muted-foreground">
													Reached as the person using the solution, with that
													person&apos;s own access there; the solution itself
													asks nothing of it.
												</span>
											)}
											{c.presence !== undefined && (
												<span className="text-muted-foreground">
													{presenceText(c.presence)}
												</span>
											)}
										</span>
									}
								>
									{c.module}
								</ListItem>
							))}
						</List>
					)}
				</div>
				<div>
					<p className="type-emphasis">Each role&apos;s reach</p>
					<ul className="list-disc pl-5 type-caption" aria-label="Reach paths">
						{offer.roles.flatMap((role) =>
							role.reach.map((row) => (
								<li
									key={`${role.name}:${row.via}:${row.path.join(">")}:${row.confinement}`}
								>
									<span className="font-mono">
										{role.name} — {row.via} {row.path.join(" → ")}
										{row.confinement ? ` (${row.confinement})` : ""}
									</span>
									{row.scopes.length > 0 && (
										<>
											{" "}
											<Scopes scopes={row.scopes} confined={false} />
										</>
									)}
								</li>
							)),
						)}
					</ul>
				</div>
				<div className="flex flex-col gap-2">
					<p className="type-emphasis">Operations each role covers</p>
					{!offer.toolsComposed && (
						<p className="text-muted-foreground">
							Agent tools are not listed: this host does not read them.
						</p>
					)}
					{groups.length === 0 ? (
						<p>No delivered module declares an operation these roles cover.</p>
					) : (
						groups.map((group) => (
							<Disclosure
								key={group.module}
								headingLevel={4}
								title={`${group.module} · ${group.rows.length} ${
									group.rows.length === 1 ? "operation" : "operations"
								} · ${accessMix(group.rows.map((row) => row.requires))}`}
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
			</div>
		</Disclosure>
	);
}

/**
 * The operations every role covers, one row per operation with a column per
 * role, grouped by module and banded by the access each demands (reads apart
 * from writes): for the technical reader, behind the disclosure.
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
		};
		group.rows.push(row);
		groups.set(row.module, group);
	}
	for (const group of groups.values()) {
		const bands = new Map<string, ReachRow[]>();
		for (const row of group.rows) {
			const level = accessLevel(row.requires);
			bands.set(level, [...(bands.get(level) ?? []), row]);
		}
		group.bands = [...bands.entries()]
			.sort(([a], [b]) => byAccess(a, b))
			.map(([level, rows]) => ({ level, rows }));
	}
	return [...groups.values()];
}

/**
 * One entry of the permission ceiling: a module the solution binds (what it
 * may ask there as itself) or one its declaration depends on (reached as the
 * person using it; the solution itself asks nothing of it).
 */
interface CeilingEntry {
	key: string;
	module: string;
	scopes: readonly OfferScope[];
	/** "required" or "optional" when the declaration depends on the module. */
	presence?: string;
}

function ceilingEntries(offer: ReviewedOffer): CeilingEntry[] {
	const entries: CeilingEntry[] = offer.ceiling.map((c) => ({
		key: `binding:${c.binding}`,
		module: c.module,
		scopes: c.scopes,
	}));
	for (const edge of offer.edgeDependencies) {
		const bound = entries.find((e) => e.module === edge.module);
		if (bound) {
			bound.presence = edge.presence;
			continue;
		}
		entries.push({
			key: `edge:${edge.module}`,
			module: edge.module,
			scopes: [],
			presence: edge.presence,
		});
	}
	return entries;
}

function presenceText(presence: string): string {
	return presence === "required"
		? "Required: the solution does not work without it."
		: presence === "optional"
			? "Optional: the solution works without it."
			: `Presence: ${presence}.`;
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
			className={
				fixedRole ? "flex flex-wrap items-end gap-3" : "flex flex-col gap-3"
			}
			aria-label={fixedRole ? `Give ${fixedRole} to someone` : undefined}
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
			{fixedRole ? null : (
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
