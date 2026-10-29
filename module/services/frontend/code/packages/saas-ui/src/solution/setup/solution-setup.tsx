"use client";

import {
	HostInstallationStatus,
	type OfferRole,
	type OfferScope,
	type ReviewedOffer,
	type RoleGrant,
} from "@codefly-dev/saas-sdk";
import {
	Badge,
	Banner,
	Button,
	Card,
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
import { type ReactNode, useMemo, useState } from "react";
import type { SolutionRequestBinding } from "../binding.js";
import { solutionTransport } from "../transport.js";
import {
	holderName,
	installed,
	isDenied,
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

function RolesSection({
	offer,
	offerError,
}: {
	offer?: ReviewedOffer;
	offerError?: unknown;
}) {
	return (
		<Card
			title="Roles and what they grant"
			description="What each role grants, limited to what this installation owns, the module operations that covers, and the agent tools those operations are. The authority checks them again on every call."
		>
			{!offer ? (
				<Refusal error={offerError} what="the roles" />
			) : (
				<div className="flex flex-col gap-4">
					{!offer.toolsComposed && (
						<Banner
							tone="info"
							title="Tool exposure is unavailable from this host."
						/>
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
										description={<Scopes scopes={c.scopes} confined={false} />}
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
						offer.roles.map((role) => (
							<RoleCard
								key={role.name}
								role={role}
								toolsComposed={offer.toolsComposed}
							/>
						))
					)}
				</div>
			)}
		</Card>
	);
}

function RoleCard({
	role,
	toolsComposed,
}: {
	role: OfferRole;
	toolsComposed: boolean;
}) {
	return (
		<Card
			titleAs="h4"
			title={`Role: ${role.name}`}
			description={<Scopes scopes={role.scopes} confined />}
		>
			{role.capabilities.length === 0 ? (
				<p>No composed operation is covered by this role.</p>
			) : (
				<Table aria-label={`What ${role.name} reaches`}>
					<TableHeader>
						<TableRow>
							<TableHead>Module</TableHead>
							<TableHead>Operation</TableHead>
							<TableHead>Requires</TableHead>
							<TableHead>Agent tools</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{role.capabilities.map((c) => (
							<TableRow key={c.module + c.operation}>
								<TableCell>{c.module}</TableCell>
								<TableCell className="whitespace-normal break-words">
									<code>{c.operation}</code>
								</TableCell>
								<TableCell className="whitespace-normal">
									<Scopes scopes={c.requires} confined={false} />
								</TableCell>
								<TableCell className="whitespace-normal">
									{c.tools.length === 0 ? (
										toolsComposed ? (
											"Not exposed as a tool"
										) : (
											"Tool exposure unavailable"
										)
									) : (
										<span className="inline-flex flex-wrap gap-1.5">
											{c.tools.map((t) => (
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
							</TableRow>
						))}
					</TableBody>
				</Table>
			)}
		</Card>
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
			{fixedRole ? <p>Role: {fixedRole}</p> : <Field label="Role">
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
			</Field>}
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
