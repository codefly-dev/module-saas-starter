"use client";

import {
	Badge,
	Button,
	Input,
	Label,
	Spinner,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { DatasourceClient, DomainView } from "./types.js";

/**
 * The datasource directory: the explicit mappings the host translates a
 * provider's per-item access lists through. Nothing here is inferred — a
 * person proves their own provider account by signing in to the provider
 * (never by email), and an administrator binds provider groups to teams and
 * verifies domains. Whatever is not mapped grants nothing.
 */

/** Marks an account-link return apart from other returns to the same page. */
const linkStatePrefix = "dl.";

const myLinksKey = (orgId: string) =>
	["datasource-account-links", orgId] as const;
const directoryKey = (orgId: string) =>
	["datasource-directory", orgId] as const;

/**
 * The browser's return from a provider sign-in, when this page is one: the
 * provider echoes the state the host minted (which says it is a link, not an
 * App setup) and appends its code.
 */
function readLinkReturn(): { state: string; code: string } | undefined {
	if (typeof window === "undefined") return undefined;
	const params = new URLSearchParams(window.location.search);
	const state = params.get("state");
	const code = params.get("code");
	return state?.startsWith(linkStatePrefix) && code
		? { state, code }
		: undefined;
}

/** Burns the state and code out of the address bar: they are single-use. */
function clearLinkReturn() {
	const params = new URLSearchParams(window.location.search);
	params.delete("state");
	params.delete("code");
	const query = params.toString();
	window.history.replaceState(
		window.history.state,
		"",
		`${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`,
	);
}

/**
 * A person's own linked provider accounts: link one by signing in to the
 * provider, remove one. Mount it on the page the provider returns to.
 */
export function DatasourceAccountLinks({
	client,
	orgId,
	connector = "github",
	providerName = "GitHub",
}: {
	client: DatasourceClient;
	orgId: string;
	connector?: string;
	providerName?: string;
}) {
	const cache = useQueryClient();
	// Read once: the return is consumed by the first render that sees it.
	const [linkReturn] = useState(readLinkReturn);
	const links = useQuery({
		queryKey: myLinksKey(orgId),
		queryFn: () => client.listMyAccountLinks!(orgId),
		enabled: !!orgId && !!client.listMyAccountLinks,
		retry: false,
	});
	// `links.data` is undefined until the answer lands, so the empty branch below
	// would otherwise state "No account is linked" about a read still in flight —
	// and go on stating it for as long as the read takes. `quiet` is the window
	// before an indicator is earned, and it must say nothing rather than fall
	// through to that claim.
	const { indicator: linksIndicator, quiet: linksQuiet } = useLoadingPhase(
		links.isPending && links.fetchStatus !== "idle",
	);
	const completed = useQuery({
		queryKey: ["datasource-account-link-return", orgId, linkReturn?.state],
		queryFn: async () => {
			try {
				return await client.completeAccountLink!(
					orgId,
					linkReturn!.state,
					linkReturn!.code,
				);
			} finally {
				clearLinkReturn();
				await cache.invalidateQueries({ queryKey: myLinksKey(orgId) });
			}
		},
		// The state is redeemable once, so the answer is never refetched.
		enabled: !!linkReturn && !!client.completeAccountLink,
		retry: false,
		staleTime: Number.POSITIVE_INFINITY,
		gcTime: Number.POSITIVE_INFINITY,
	});
	const begin = useMutation({
		mutationFn: () => {
			const back = `${window.location.origin}${window.location.pathname}`;
			return client.beginAccountLink!(orgId, connector, back);
		},
		onSuccess: (handle) => window.location.assign(handle.authorizeUrl),
	});
	const remove = useMutation({
		mutationFn: (id: string) => client.deleteAccountLink!(orgId, id),
		onSuccess: () => cache.invalidateQueries({ queryKey: myLinksKey(orgId) }),
	});

	if (!client.listMyAccountLinks) return null;
	const mine = (links.data ?? []).filter((l) => l.connector === connector);
	return (
		<section
			aria-label={`Your ${providerName} account`}
			className="space-y-3 rounded border p-4"
		>
			<h3 className="type-section-title">Your {providerName} account</h3>
			<p className="type-body">
				Linking proves which {providerName} account is yours, so content shared
				with that account there is readable by you here. You sign in to{" "}
				{providerName}; your email address is never used to match you.
			</p>
			{completed.isSuccess && (
				<p role="status" className="type-body">
					Linked {providerName} account {completed.data.providerAccountLogin}.
				</p>
			)}
			{completed.isError && (
				<p role="alert" className="type-body">
					Couldn’t link the account: {completed.error.message}
				</p>
			)}
			{links.isError ? (
				<p role="alert" className="type-body">
					Couldn’t load your linked accounts.
				</p>
			) : linksQuiet ? null : linksIndicator ? (
				<Spinner label="Loading your linked accounts" size="sm" />
			) : mine.length === 0 ? (
				<p className="type-body">No {providerName} account is linked.</p>
			) : (
				<ul className="space-y-2">
					{mine.map((l) => (
						<li key={l.id} className="flex items-center gap-2">
							<Badge variant="secondary">
								{l.providerAccountLogin || l.providerAccountId}
							</Badge>
							<Button
								variant="outline"
								size="sm"
								disabled={remove.isPending}
								onClick={() => remove.mutate(l.id)}
							>
								Unlink
							</Button>
						</li>
					))}
				</ul>
			)}
			{client.beginAccountLink && (
				<Button disabled={begin.isPending} onClick={() => begin.mutate()}>
					Link your {providerName} account
				</Button>
			)}
			{begin.isError && (
				<p role="alert" className="type-body">
					Couldn’t start the link: {begin.error.message}
				</p>
			)}
			{remove.isError && (
				<p role="alert" className="type-body">
					Couldn’t unlink: {remove.error.message}
				</p>
			)}
		</section>
	);
}

/**
 * An administrator's view of the organization's directory: every linked
 * account, provider group bindings, and claimed domains with the DNS record
 * that verifies each.
 */
export function DatasourceDirectoryPanel({
	client,
	orgId,
	connector = "github",
	groupHint = "organization/team, e.g. acme/platform",
}: {
	client: DatasourceClient;
	orgId: string;
	connector?: string;
	groupHint?: string;
}) {
	const cache = useQueryClient();
	const [group, setGroup] = useState("");
	const [teamId, setTeamId] = useState("");
	const [domain, setDomain] = useState("");
	const directory = useQuery({
		queryKey: directoryKey(orgId),
		queryFn: () => client.getDirectory!(orgId),
		enabled: !!orgId && !!client.getDirectory,
		retry: false,
	});
	// `fetchStatus !== "idle"` because a disabled query also sits at isPending,
	// and a wait that is not happening must not earn an indicator.
	const { indicator: directoryIndicator, quiet: directoryQuiet } =
		useLoadingPhase(directory.isPending && directory.fetchStatus !== "idle");
	const refresh = () =>
		cache.invalidateQueries({ queryKey: directoryKey(orgId) });
	const bind = useMutation({
		mutationFn: () => client.bindGroup!(orgId, connector, group.trim(), teamId),
		onSuccess: async () => {
			setGroup("");
			setTeamId("");
			await refresh();
		},
	});
	const unbind = useMutation({
		mutationFn: (id: string) => client.unbindGroup!(orgId, id),
		onSuccess: refresh,
	});
	const claim = useMutation({
		mutationFn: () => client.claimDomain!(orgId, domain.trim()),
		onSuccess: async () => {
			setDomain("");
			await refresh();
		},
	});
	const verify = useMutation({
		mutationFn: (d: DomainView) => client.verifyDomain!(orgId, d.id),
		onSuccess: refresh,
	});
	const removeDomain = useMutation({
		mutationFn: (id: string) => client.deleteDomain!(orgId, id),
		onSuccess: refresh,
	});
	const unlink = useMutation({
		mutationFn: (id: string) => client.deleteAccountLink!(orgId, id),
		onSuccess: refresh,
	});

	if (!client.getDirectory) return null;
	if (directory.isError)
		return (
			<p role="alert" className="type-body">
				Couldn’t load the datasource directory.
			</p>
		);
	if (directoryQuiet) return null;
	if (directoryIndicator)
		return <Spinner label="Loading the datasource directory" size="sm" />;
	if (directory.isPending) return null;
	const data = directory.data;
	const teamName = (id: string) =>
		data.teams.find((t) => t.id === id)?.name ?? id;
	const failure = [bind, unbind, claim, verify, removeDomain, unlink].find(
		(m) => m.isError,
	);
	return (
		<section aria-label="Datasource directory" className="space-y-6">
			<p className="type-body">
				A source that shares items with particular people or groups is read here
				only through these mappings. Anything not mapped grants nothing.
			</p>
			{failure?.error && (
				<p role="alert" className="type-body">
					{failure.error.message}
				</p>
			)}

			<div className="space-y-2">
				<h3 className="type-section-title">Provider groups</h3>
				{data.bindings.length === 0 ? (
					<p className="type-body">No provider group is bound to a team.</p>
				) : (
					<ul className="space-y-2">
						{data.bindings.map((b) => (
							<li key={b.id} className="flex items-center gap-2">
								<span className="type-body">
									{b.providerGroupId} → {teamName(b.teamId)}
								</span>
								<Button
									variant="outline"
									size="sm"
									disabled={unbind.isPending}
									onClick={() => unbind.mutate(b.id)}
								>
									Unbind
								</Button>
							</li>
						))}
					</ul>
				)}
				<div className="flex flex-wrap items-end gap-2">
					<div className="space-y-1">
						<Label htmlFor="directory-group">Provider group</Label>
						<Input
							id="directory-group"
							value={group}
							placeholder={groupHint}
							onChange={(event) => setGroup(event.target.value)}
						/>
					</div>
					<label className="type-body">
						Team{" "}
						<select
							aria-label="Team"
							value={teamId}
							onChange={(event) => setTeamId(event.target.value)}
						>
							<option value="">Choose a team</option>
							{data.teams.map((t) => (
								<option key={t.id} value={t.id}>
									{t.name}
								</option>
							))}
						</select>
					</label>
					<Button
						disabled={!group.trim() || !teamId || bind.isPending}
						onClick={() => bind.mutate()}
					>
						Bind group
					</Button>
				</div>
			</div>

			<div className="space-y-2">
				<h3 className="type-section-title">Verified domains</h3>
				<p className="type-body">
					“Anyone in the domain” at a provider reads here only for a domain
					verified to this organization.
				</p>
				{data.domains.length === 0 ? (
					<p className="type-body">No domain is claimed.</p>
				) : (
					<ul className="space-y-3">
						{data.domains.map((d) => (
							<li key={d.id} className="space-y-1">
								<div className="flex items-center gap-2">
									<span className="type-body">{d.domain}</span>
									<Badge variant={d.verified ? "secondary" : "outline"}>
										{d.verified ? "Verified" : "Pending"}
									</Badge>
									{!d.verified && (
										<Button
											variant="outline"
											size="sm"
											disabled={verify.isPending}
											onClick={() => verify.mutate(d)}
										>
											Verify
										</Button>
									)}
									<Button
										variant="outline"
										size="sm"
										disabled={removeDomain.isPending}
										onClick={() => removeDomain.mutate(d.id)}
									>
										Remove
									</Button>
								</div>
								{!d.verified && (
									<p className="type-caption-plain">
										Publish a TXT record named <code>{d.txtRecordName}</code>{" "}
										with the value <code>{d.txtRecordValue}</code>, then verify.
									</p>
								)}
							</li>
						))}
					</ul>
				)}
				<div className="flex flex-wrap items-end gap-2">
					<div className="space-y-1">
						<Label htmlFor="directory-domain">Domain</Label>
						<Input
							id="directory-domain"
							value={domain}
							placeholder="example.com"
							onChange={(event) => setDomain(event.target.value)}
						/>
					</div>
					<Button
						disabled={!domain.trim() || claim.isPending}
						onClick={() => claim.mutate()}
					>
						Claim domain
					</Button>
				</div>
			</div>

			<div className="space-y-2">
				<h3 className="type-section-title">Linked accounts</h3>
				{data.links.length === 0 ? (
					<p className="type-body">No member has linked a provider account.</p>
				) : (
					<ul className="space-y-2">
						{data.links.map((l) => (
							<li key={l.id} className="flex items-center gap-2">
								<span className="type-body">
									{l.connector} {l.providerAccountLogin || l.providerAccountId}
								</span>
								<Button
									variant="outline"
									size="sm"
									disabled={unlink.isPending}
									onClick={() => unlink.mutate(l.id)}
								>
									Remove link
								</Button>
							</li>
						))}
					</ul>
				)}
			</div>
		</section>
	);
}
