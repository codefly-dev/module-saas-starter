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
import { useEffect, useState } from "react";
import { apiSourceRefusal, sourceLinks } from "./api-source.js";
import { selectClass } from "./collection-field.js";
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
 * App setup) and appends its code — or, when the person declined or the
 * provider refused, an OAuth `error` in place of the code.
 */
type LinkReturn =
	| { state: string; code: string }
	| { state: string; error: string };

function readLinkReturn(): LinkReturn | undefined {
	if (typeof window === "undefined") return undefined;
	const params = new URLSearchParams(window.location.search);
	const state = params.get("state");
	if (!state?.startsWith(linkStatePrefix)) return undefined;
	const code = params.get("code");
	if (code) return { state, code };
	const error = params.get("error");
	return error ? { state, error } : undefined;
}

/** Burns the return out of the address bar: its state and code are single-use. */
function clearLinkReturn() {
	const params = new URLSearchParams(window.location.search);
	for (const name of [
		"state",
		"code",
		"error",
		"error_description",
		"error_uri",
	]) {
		params.delete(name);
	}
	const query = params.toString();
	window.history.replaceState(
		window.history.state,
		"",
		`${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`,
	);
}

/**
 * The calling person's linked provider accounts — the one read both
 * `DatasourceAccountLinks` and a card that shows whether its source is signed
 * in make, under one key, so a completed link refreshes both.
 */
export function useMyAccountLinks(client: DatasourceClient, orgId: string) {
	return useQuery({
		queryKey: myLinksKey(orgId),
		queryFn: () => client.listMyAccountLinks!(orgId),
		enabled: !!orgId && !!client.listMyAccountLinks,
		retry: false,
	});
}

/**
 * Sends the browser to the provider to sign in, returning to this page: the
 * host checks the provider's code against the redirect it minted the state
 * for, so the page that begins the link is the page that completes it.
 */
export async function beginAccountLinkHere(
	client: DatasourceClient,
	orgId: string,
	connector: string,
	sourceId?: string,
): Promise<void> {
	const back = `${window.location.origin}${window.location.pathname}`;
	const handle = await client.beginAccountLink!(
		orgId,
		connector,
		back,
		sourceId,
	);
	window.location.assign(handle.authorizeUrl);
}

/**
 * A person's own linked provider accounts: link one by signing in to the
 * provider, remove one. Mount it on the page the provider returns to.
 *
 * With `sourceId` it links that one source instead — a generic OAuth `api`
 * source (`connector="api"`) the person created for themselves: it begins the
 * sign-in with the source, and lists only that source's link. Such a sign-in
 * authorizes the source to call the provider as the person; it proves nothing
 * about who they are there, so nothing is said about matching accounts. It
 * offers no Unlink: removing the link leaves the source's authorization in
 * place, so a control that only removed the record would claim a disconnect
 * that did not happen.
 */
export function DatasourceAccountLinks({
	client,
	orgId,
	connector = "github",
	providerName = "GitHub",
	sourceId,
	beforeLink,
}: {
	client: DatasourceClient;
	orgId: string;
	connector?: string;
	providerName?: string;
	/** The source a generic OAuth link authorizes; required by the host for `api`. */
	sourceId?: string;
	/**
	 * Runs before the sign-in begins; a rejection stops it and is reported as
	 * the link's failure. A card uses it to declare the source's operations, so
	 * a retry after a failed declaration declares them again.
	 */
	beforeLink?: () => Promise<void>;
}) {
	const cache = useQueryClient();
	// Read once: the return is consumed by the first render that sees it.
	const [linkReturn] = useState(readLinkReturn);
	const links = useMyAccountLinks(client, orgId);
	// `links.data` is undefined until the answer lands, so the empty branch below
	// would otherwise state "No account is linked" about a read still in flight —
	// and go on stating it for as long as the read takes. `quiet` is the window
	// before an indicator is earned, and it must say nothing rather than fall
	// through to that claim.
	const { indicator: linksIndicator, quiet: linksQuiet } = useLoadingPhase(
		links.isPending && links.fetchStatus !== "idle",
	);
	const redeemable =
		linkReturn && "code" in linkReturn ? linkReturn : undefined;
	const completed = useQuery({
		queryKey: ["datasource-account-link-return", orgId, redeemable?.state],
		queryFn: async () => {
			try {
				return await client.completeAccountLink!(
					orgId,
					redeemable!.state,
					redeemable!.code,
				);
			} finally {
				clearLinkReturn();
				await cache.invalidateQueries({ queryKey: myLinksKey(orgId) });
			}
		},
		// The state is redeemable once, so the answer is never refetched.
		enabled: !!redeemable && !!client.completeAccountLink,
		retry: false,
		staleTime: Number.POSITIVE_INFINITY,
		gcTime: Number.POSITIVE_INFINITY,
	});
	// A refused sign-in carries nothing to redeem; its parameters still go, so a
	// reload does not report the same refusal again.
	const declined = linkReturn && "error" in linkReturn ? linkReturn : undefined;
	useEffect(() => {
		if (declined) clearLinkReturn();
	}, [declined]);
	const begin = useMutation({
		mutationFn: async () => {
			await beforeLink?.();
			await beginAccountLinkHere(client, orgId, connector, sourceId);
		},
	});
	const remove = useMutation({
		mutationFn: (id: string) => client.deleteAccountLink!(orgId, id),
		onSuccess: () => cache.invalidateQueries({ queryKey: myLinksKey(orgId) }),
	});

	if (!client.listMyAccountLinks) return null;
	if (sourceId !== undefined) {
		const mine = sourceLinks(links.data ?? [], sourceId);
		return (
			<section
				aria-label={`Your ${providerName} sign-in`}
				className="space-y-3 rounded border p-4"
			>
				<h3 className="type-section-title">Your {providerName} sign-in</h3>
				<p className="type-body">
					Signing in lets this source call {providerName} as you. The source is
					yours: no one else in the organization can use it.
				</p>
				{completed.isSuccess && (
					<p role="status" className="type-body">
						Signed in to {providerName}.
					</p>
				)}
				{completed.isError && (
					<p role="alert" className="type-body">
						Couldn’t finish signing in:{" "}
						{apiSourceRefusal(completed.error, providerName)}
					</p>
				)}
				{declined && (
					<p role="alert" className="type-body">
						The sign-in at {providerName} did not finish ({declined.error}).
						Start it again.
					</p>
				)}
				{links.isError ? (
					<p role="alert" className="type-body">
						Couldn’t check whether you are signed in.
					</p>
				) : linksQuiet ? null : linksIndicator ? (
					<Spinner label="Checking your sign-in" size="sm" />
				) : mine.length === 0 ? (
					<p className="type-body">You have not signed in yet.</p>
				) : (
					<p className="type-body">
						Signed in. Signing in again replaces the authorization. To stop this
						source calling {providerName} as you, revoke its access at{" "}
						{providerName}, or remove the source.
					</p>
				)}
				{client.beginAccountLink && (
					<Button disabled={begin.isPending} onClick={() => begin.mutate()}>
						{mine.length === 0 ? `Sign in to ${providerName}` : "Sign in again"}
					</Button>
				)}
				{begin.isError && (
					<p role="alert" className="type-body">
						Couldn’t start the sign-in:{" "}
						{apiSourceRefusal(begin.error, providerName)}
					</p>
				)}
			</section>
		);
	}
	const mine = (links.data ?? []).filter((l) => l.connector === connector);
	return (
		<section aria-label={`Your ${providerName} account`} className="space-y-2">
			<h4 className="type-label">Your {providerName} account</h4>
			<p className="type-caption-plain text-muted-foreground">
				Link it so what is shared with you on {providerName} is readable here
				too. You sign in to {providerName}; your email is never used to match
				you.
			</p>
			{completed.isSuccess && (
				<p role="status" className="type-body">
					Linked as {completed.data.providerAccountLogin}.
				</p>
			)}
			{completed.isError && (
				<p role="alert" className="type-body text-destructive">
					Couldn’t link the account: {completed.error.message}
				</p>
			)}
			{declined && (
				<p role="alert" className="type-body text-destructive">
					The sign-in at {providerName} did not finish. Start it again.
				</p>
			)}
			{links.isError ? (
				<p role="alert" className="type-body text-destructive">
					Couldn’t load your linked account.
				</p>
			) : linksQuiet ? null : linksIndicator ? (
				<Spinner label="Loading your linked account" size="sm" />
			) : mine.length === 0 ? (
				client.beginAccountLink && (
					<Button
						size="sm"
						variant="outline"
						disabled={begin.isPending}
						onClick={() => begin.mutate()}
					>
						Link your {providerName} account
					</Button>
				)
			) : (
				<ul className="space-y-2">
					{mine.map((l) => (
						<li key={l.id} className="flex flex-wrap items-center gap-2">
							<span className="type-body">
								Linked as {l.providerAccountLogin || "your account"}
							</span>
							<Button
								variant="ghost"
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
			{begin.isError && (
				<p role="alert" className="type-body text-destructive">
					Couldn’t start the link: {begin.error.message}
				</p>
			)}
			{remove.isError && (
				<p role="alert" className="type-body text-destructive">
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
	providerName = "GitHub",
	groupHint = "acme/platform",
}: {
	client: DatasourceClient;
	orgId: string;
	connector?: string;
	providerName?: string;
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
			<p role="alert" className="type-body text-destructive">
				Couldn’t load the {providerName} matching settings.
			</p>
		);
	if (directoryQuiet) return null;
	if (directoryIndicator)
		return <Spinner label={`Loading the ${providerName} settings`} size="sm" />;
	if (directory.isPending) return null;
	const data = directory.data;
	const teamName = (id: string) =>
		data.teams.find((t) => t.id === id)?.name ?? "A removed team";
	const failure = [bind, unbind, claim, verify, removeDomain, unlink].find(
		(m) => m.isError,
	);
	const id = (name: string) => `directory-${orgId}-${name}`;
	return (
		<div className="space-y-6">
			{failure?.error && (
				<p role="alert" className="type-body text-destructive">
					{failure.error.message}
				</p>
			)}

			<section aria-labelledby={id("groups")} className="space-y-2">
				<h4 id={id("groups")} className="type-label">
					{providerName} teams
				</h4>
				<p className="type-caption-plain text-muted-foreground">
					Give a {providerName} team&apos;s members what one of your teams can read.
				</p>
				{data.bindings.length > 0 && (
					<ul className="divide-y rounded-md border">
						{data.bindings.map((b) => (
							<li
								key={b.id}
								className="flex flex-wrap items-center justify-between gap-2 px-3 py-2"
							>
								<span className="type-body break-all">
									{b.providerGroupId} → {teamName(b.teamId)}
								</span>
								<Button
									variant="ghost"
									size="sm"
									disabled={unbind.isPending}
									onClick={() => unbind.mutate(b.id)}
								>
									Remove
								</Button>
							</li>
						))}
					</ul>
				)}
				<div className="flex flex-wrap items-end gap-2">
					<div className="min-w-48 flex-1 space-y-1">
						<Label htmlFor={id("group")}>{providerName} team</Label>
						<Input
							id={id("group")}
							value={group}
							placeholder={groupHint}
							onChange={(event) => setGroup(event.target.value)}
						/>
					</div>
					<div className="min-w-40 flex-1 space-y-1">
						<Label htmlFor={id("team")}>Your team</Label>
						<select
							id={id("team")}
							className={selectClass}
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
					</div>
					<Button
						variant="outline"
						disabled={!group.trim() || !teamId || bind.isPending}
						onClick={() => bind.mutate()}
					>
						Match
					</Button>
				</div>
			</section>

			<section aria-labelledby={id("domains")} className="space-y-2">
				<h4 id={id("domains")} className="type-label">
					Verified domains
				</h4>
				<p className="type-caption-plain text-muted-foreground">
					Prove you own a domain, so what is shared with everyone at it on{" "}
					{providerName} is readable here.
				</p>
				{data.domains.length > 0 && (
					<ul className="divide-y rounded-md border">
						{data.domains.map((d) => (
							<li key={d.id} className="space-y-1 px-3 py-2">
								<div className="flex flex-wrap items-center gap-2">
									<span className="type-body">{d.domain}</span>
									<Badge tone={d.verified ? "success" : "warning"} dot>
										{d.verified ? "Verified" : "Waiting for DNS"}
									</Badge>
									<span className="ml-auto flex gap-1">
										{!d.verified && (
											<Button
												variant="outline"
												size="sm"
												disabled={verify.isPending}
												onClick={() => verify.mutate(d)}
											>
												Check now
											</Button>
										)}
										<Button
											variant="ghost"
											size="sm"
											disabled={removeDomain.isPending}
											onClick={() => removeDomain.mutate(d.id)}
										>
											Remove
										</Button>
									</span>
								</div>
								{!d.verified && (
									<p className="type-caption-plain text-muted-foreground break-all">
										Add a TXT record named <code>{d.txtRecordName}</code> with
										the value <code>{d.txtRecordValue}</code>, then check.
									</p>
								)}
							</li>
						))}
					</ul>
				)}
				<div className="flex flex-wrap items-end gap-2">
					<div className="min-w-48 flex-1 space-y-1">
						<Label htmlFor={id("domain")}>Domain</Label>
						<Input
							id={id("domain")}
							value={domain}
							placeholder="example.com"
							onChange={(event) => setDomain(event.target.value)}
						/>
					</div>
					<Button
						variant="outline"
						disabled={!domain.trim() || claim.isPending}
						onClick={() => claim.mutate()}
					>
						Add domain
					</Button>
				</div>
			</section>

			{data.links.length > 0 && (
				<section aria-labelledby={id("links")} className="space-y-2">
					<h4 id={id("links")} className="type-label">
						Members&apos; linked accounts
					</h4>
					<p className="type-caption-plain text-muted-foreground">
						Members who linked their {providerName} account. Removing one stops
						what is shared with them there from reaching them here.
					</p>
					<ul className="divide-y rounded-md border">
						{data.links.map((l) => (
							<li
								key={l.id}
								className="flex flex-wrap items-center justify-between gap-2 px-3 py-2"
							>
								<span className="type-body">
									{l.providerAccountLogin || "An unnamed account"}
								</span>
								<Button
									variant="ghost"
									size="sm"
									disabled={unlink.isPending}
									onClick={() => unlink.mutate(l.id)}
								>
									Remove
								</Button>
							</li>
						))}
					</ul>
				</section>
			)}
		</div>
	);
}
