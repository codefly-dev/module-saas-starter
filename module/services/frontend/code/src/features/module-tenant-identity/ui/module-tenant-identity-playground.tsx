"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { KeyRound } from "lucide-react";
import { useState } from "react";
import { EmptyState } from "@/components/empty-state";
import {
	readHasNotAnswered,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	DelayedLoading,
	Input,
	Label,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@/shared/ui";
import { useModuleTenantIdentity } from "../service/queries";

/**
 * The playground for a module identity minted for ONE tenant.
 *
 * A composed module acting with no person present mints a Work Context for one
 * of its installed operation bindings and names the organization it is acting
 * FOR. That capability is how a module presents itself as acting for a tenant
 * rather than as itself, and it is what a consumer admitting a publication for
 * a tenant needs from a publisher with nobody signed in: the tenant comes from
 * the verified capability, where this host has already checked that the
 * principal may speak for it, never from a request field.
 *
 * Three things bound it, and the page shows each of them answered:
 *
 *   - the ENVELOPE decides which organizations may be named. A principal pinned
 *     to a tenant may name that tenant and no other; an unpinned principal
 *     declaring `cross_tenant` may name one organization per mint. Naming an
 *     organization is not authority over it.
 *   - that organization's own live INSTALLATION must consent to this module.
 *   - the binding must declare `headless_scopes`, and what the capability
 *     carries is that list narrowed by THIS organization's installation scope.
 *     An organization that consented to less gets less, and one that consented
 *     to nothing is refused — which is what makes the bound per tenant rather
 *     than per deployment.
 *
 * IT MINTS NOTHING, deliberately. The mint authenticates with the module pod's
 * projected ServiceAccount token, held against the build its authority document
 * approves; a browser holds no such token, and a surface that handed an operator
 * a module's capability would be a second spelling of that module's identity. So
 * this resolves the decision from the same declared facts the mint decides on
 * and stops one step before the signature. What it then shows beside the
 * resolution is the trail of capabilities actually issued, which is the only
 * record of one: a capability lives about a minute and nothing caches it.
 */
export function ModuleTenantIdentityPlayground() {
	const [prefix, setPrefix] = useState("");
	const [binding, setBinding] = useState("");
	const [organizationId, setOrganizationId] = useState("");
	const read = useModuleTenantIdentity(prefix, binding, organizationId);
	// `empty` is this classifier's word for "the read completed" — there is no
	// list here, so the only thing it is asked is whether the answer arrived.
	const outcome = readOutcome(read.isError, read.error);
	const answer = read.data;
	// A disabled query is not a loading one, a paused one is not finished, and
	// a refused refetch's paused retry is neither — asked once, in the read
	// seam, rather than re-derived from whichever flag looked closest (issue
	// #1121). A read that was never issued is idle, which is what keeps
	// "complete the selection above" attached to an incomplete selection.
	const unanswered = readHasNotAnswered(read);
	const phase = useLoadingPhase(unanswered);
	const waiting = unanswered || phase.indicator || phase.quiet;
	const stale = answer
		? staleReadNotice(
				outcome,
				"this module’s authority and issued capabilities",
			)
		: null;

	return (
		<div className="space-y-6">
			<div>
				<h1 data-slot="page-title" className="type-page-title">
					Module identity for one tenant
				</h1>
				<p className="text-muted-foreground">
					A composed module with no person present mints a capability for one of
					its installed operation bindings and names the organization it acts
					for. Resolve what that capability would carry — and why it would be
					refused.
				</p>
			</div>

			<Card>
				<CardHeader>
					<CardTitle>What would be minted</CardTitle>
					<CardDescription>
						Name a module&apos;s registration prefix, one of its operation
						bindings, and the organization it would act for. This page reads the
						decision and issues no capability: a mint authenticates with the
						module pod&apos;s projected ServiceAccount token, which a browser
						does not have and must not be given a way around.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="flex flex-wrap items-end gap-4">
						<div className="space-y-2">
							<Label htmlFor="module-tenant-prefix">Module prefix</Label>
							<Input
								id="module-tenant-prefix"
								className="w-56"
								value={prefix}
								onChange={(event) => setPrefix(event.target.value.trim())}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="module-tenant-binding">Operation binding</Label>
							<Input
								id="module-tenant-binding"
								className="w-56"
								value={binding}
								onChange={(event) => setBinding(event.target.value.trim())}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="module-tenant-org">Organization</Label>
							<Input
								id="module-tenant-org"
								className="w-96"
								value={organizationId}
								onChange={(event) =>
									setOrganizationId(event.target.value.trim())
								}
							/>
						</div>
						<Button
							variant="outline"
							disabled={!prefix || !binding || !organizationId}
							onClick={() => void read.refetch()}
						>
							Resolve again
						</Button>
					</div>

					<DelayedLoading
						active={unanswered}
						label="Resolving the declared authority"
					/>
					{stale && (
						<p role="status" className="text-sm text-muted-foreground">
							{stale}
						</p>
					)}
					{waiting ? null : outcome !== "empty" && !answer ? (
						<p role="status" className="text-sm text-muted-foreground">
							{readOutcomeMessage(
								outcome,
								"this module's declared authority",
								"",
							)}
						</p>
					) : !answer ? (
						<EmptyState
							icon={KeyRound}
							title="Name a module, a binding and an organization"
							description="The answer is about the three together: a capability is this module's, for this binding, acting for this organization."
						/>
					) : answer.identity ? (
						<Minted identity={answer.identity} />
					) : (
						<Refused refusal={answer.refusal} />
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle>Capabilities issued</CardTitle>
					<CardDescription>
						Every mint is recorded, and the record is the only trace: a
						capability lives about a minute and nothing caches one. A reference
						that no longer resolves shows up as the next mint having no row here
						at all.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<DelayedLoading
						active={unanswered}
						label="Reading issued capabilities"
					/>
					{waiting ? null : !answer ? (
						<p role="status" className="text-sm text-muted-foreground">
							{readOutcomeMessage(
								outcome,
								"issued capabilities",
								"Complete the selection above to read issued capabilities.",
							)}
						</p>
					) : answer.recent.length === 0 ? (
						<p
							role="status"
							className="py-6 text-center text-sm text-muted-foreground"
						>
							No capability has been issued to this module for this
							organization.
						</p>
					) : (
						<Table data-slot="module-tenant-mints">
							<TableHeader>
								<TableRow>
									<TableHead>Minted</TableHead>
									<TableHead>Binding</TableHead>
									<TableHead>Audience</TableHead>
									<TableHead>Scopes</TableHead>
									<TableHead>Record</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{answer.recent.map((mint) => (
									<TableRow key={mint.record} data-slot="module-tenant-mint">
										<TableCell className="text-sm">
											{mint.mintedAt
												? timestampDate(mint.mintedAt).toLocaleString()
												: "—"}
										</TableCell>
										<TableCell className="font-mono text-xs">
											{mint.binding}
										</TableCell>
										<TableCell className="font-mono text-xs">
											{mint.audience}
										</TableCell>
										<TableCell className="font-mono text-xs break-all">
											{mint.scopes.join(", ") || "—"}
										</TableCell>
										<TableCell className="font-mono text-xs break-all">
											{mint.record}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>
		</div>
	);
}

/** What one per-tenant capability would assert, field by field. */
function Minted({
	identity,
}: {
	identity: {
		principalId: string;
		tenant: string;
		audience: string;
		scopes: readonly string[];
		lifetimeSeconds: bigint;
		revision: bigint;
		shared: boolean;
	};
}) {
	return (
		<div className="space-y-3" data-slot="module-tenant-identity" role="status">
			<Badge tone="success">A capability would be issued</Badge>
			<dl className="grid gap-3 text-sm sm:grid-cols-3">
				<Fact label="Owner and actor" value={identity.principalId} mono />
				<Fact label="Acting for" value={identity.tenant} mono />
				<Fact label="Audience" value={identity.audience} mono />
				<Fact
					label="Lifetime"
					value={`${identity.lifetimeSeconds.toString()} seconds`}
				/>
				<Fact
					label="Authorization revision"
					value={identity.revision.toString()}
					mono
				/>
				<Fact
					label="Tenancy"
					value={
						identity.shared
							? "Shared — may act for any organization that installs it"
							: "Pinned — may act only for its declared tenant"
					}
				/>
			</dl>
			<div>
				<p className="text-xs text-muted-foreground">
					Scopes, as this organization&apos;s installation narrowed them
				</p>
				<p
					className="font-mono text-xs break-all"
					data-slot="module-tenant-scopes"
				>
					{identity.scopes.join(", ") || "—"}
				</p>
			</div>
			<p className="text-xs text-muted-foreground">
				The owner is the module&apos;s own service principal and it acts
				directly: no person, and no actor hop. The scopes are the binding&apos;s{" "}
				<code>headless_scopes</code> narrowed by this organization&apos;s
				installation scope, never the <code>invoke_scopes</code> it holds for
				work a person asked for.
			</p>
		</div>
	);
}

/**
 * Why nothing would be minted. The refusal is the host's own words rather than
 * a code, because each one is a different thing for an operator to do: declare
 * the principal, widen the envelope's tenancy, install the module in that
 * organization, or consent to the binding's headless scopes there.
 */
function Refused({ refusal }: { refusal: string }) {
	return (
		<div className="space-y-2" data-slot="module-tenant-refusal" role="status">
			<Badge tone="danger">No capability would be issued</Badge>
			<p className="text-sm">{refusal}</p>
			<p className="text-xs text-muted-foreground">
				A refusal here is a decision about declared authority, not an outage: it
				is what the mint itself would answer, and retrying changes nothing until
				the declaration does.
			</p>
		</div>
	);
}

/** One labelled fact. */
function Fact({
	label,
	value,
	mono,
}: {
	label: string;
	value: string;
	mono?: boolean;
}) {
	return (
		<div>
			<dt className="text-xs text-muted-foreground">{label}</dt>
			<dd className={mono ? "font-mono text-xs break-all" : "text-sm"}>
				{value}
			</dd>
		</div>
	);
}
