"use client";

import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { Check, Info, Undo2, UsersRound } from "lucide-react";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { toast } from "sonner";
import { teamQueries } from "@/features/teams/service/queries";
import { teamInstallationRoles } from "@/features/teams/ui/team-installation-access";
import { hostRefusal } from "@/features/solution-installations/model/installations";
import {
	useAuthorityInstallations,
	useRoleGrants,
} from "@/features/solution-installations/service/queries";
import { useTargetTitles } from "@/features/solution-installations/ui/target-name";
import { InstallationService } from "@/gen/host/installation/v1/installation_pb";
import { SessionService } from "@/gen/host/session/v1/session_pb";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { hasPermission } from "@/lib/permissions";
import {
	Badge,
	Button,
	Command,
	CommandEmpty,
	CommandGroup,
	CommandInput,
	CommandItem,
	CommandList,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	Popover,
	PopoverContent,
	PopoverTrigger,
	useCommandFilter,
} from "@/shared/ui";

const sessions = createClient(SessionService, apiTransport);
const installations = createClient(InstallationService, apiTransport);

/** One role a team holds on one installation, named as the person's menu names it. */
export interface Holding {
	installationId: string;
	target: string;
	title: string;
	role: string;
}

/** A team of the organization and what it holds on what is installed. */
export interface TeamHoldings {
	id: string;
	name: string;
	holds: Holding[];
}

/** "Wiki · editor" — one holding in the words the menu uses. */
export const holdingLabel = (h: Pick<Holding, "title" | "role">) =>
	`${h.title} · ${h.role}`;

// What this tab last read of the organization's teams and their holdings, so
// a cast (which reads none of it: it holds nothing of the person's) can still
// list the teams to switch to. Display data only, per tab like the cast: the
// host decides every cast opened from it.
const DIRECTORY_KEY = "codefly_cast_directory";

function keepDirectory(orgId: string, teams: TeamHoldings[]) {
	try {
		sessionStorage.setItem(DIRECTORY_KEY, JSON.stringify({ orgId, teams }));
	} catch {
		// A tab that keeps nothing still casts; it only cannot switch while cast.
	}
}

function keptDirectory(orgId: string): TeamHoldings[] {
	try {
		const kept = JSON.parse(
			sessionStorage.getItem(DIRECTORY_KEY) ?? "null",
		) as { orgId: string; teams: TeamHoldings[] } | null;
		return kept?.orgId === orgId && Array.isArray(kept.teams) ? kept.teams : [];
	} catch {
		return [];
	}
}

/**
 * The organization's teams with what each holds on its installations, read
 * from the same answers the team pages read (ListTeams, the installations and
 * their role grants), for someone who may read those. Whether the person may
 * act as a team is never worked out here: the host decides when they ask.
 * While a cast is open, what this tab read before it is answered instead.
 */
export function useTeamDirectory(): {
	teams: TeamHoldings[];
	loading: boolean;
	kept: boolean;
} {
	const {
		organizationId = "",
		platformRole,
		orgRole,
		castTeamId,
		sessionRead,
	} = useAuth();
	const own = sessionRead === "confirmed" && !castTeamId;
	// The installations and their grants are an administrator's read (the team
	// page leaves the same section out for a member rather than refuse it).
	const readable =
		own && hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE)
			? organizationId
			: "";
	const list = useQuery({ ...teamQueries.list(readable), enabled: !!readable });
	const held = useAuthorityInstallations(readable);
	const ids = [...(held.data?.keys() ?? [])];
	const { grants, pending } = useRoleGrants(readable, ids);
	const titles = useTargetTitles();
	const read = useMemo(() => {
		if (!list.data || !held.data || pending) return undefined;
		const status = new Map(
			[...held.data].map(([id, i]) => [
				id,
				{ target: i.target, status: i.status },
			]),
		);
		return list.data.teams
			.map((team) => ({
				id: team.id,
				name: team.name,
				holds: teamInstallationRoles(team.id, status, grants).map((h) => ({
					...h,
					title: titles.get(h.target) ?? h.target,
				})),
			}))
			.sort((a, b) => a.name.localeCompare(b.name));
	}, [list.data, held.data, pending, grants, titles]);
	useEffect(() => {
		if (read && organizationId) keepDirectory(organizationId, read);
	}, [read, organizationId]);
	if (castTeamId)
		return { teams: keptDirectory(organizationId), loading: false, kept: true };
	return {
		teams: read ?? [],
		loading: !!readable && !read && !list.isError && !held.isError,
		kept: false,
	};
}

// One switcher per page, opened from the account menu, the cast bar or a team.
let switcherOpen = false;
const switcherListeners = new Set<() => void>();
export function setTeamSwitcherOpen(open: boolean) {
	switcherOpen = open;
	for (const listener of switcherListeners) listener();
}
function useTeamSwitcherOpen() {
	return useSyncExternalStore(
		(listener) => {
			switcherListeners.add(listener);
			return () => switcherListeners.delete(listener);
		},
		() => switcherOpen,
		() => false,
	);
}

/** A refusal in the host's words with its code, as every cast refusal is shown. */
export const castRefusal = (error: unknown) =>
	`${hostRefusal(error)} (${Code[ConnectError.from(error).code]})`;

/**
 * Opens a cast onto a team, from wherever the person is. A session that is
 * already a cast (or a role view) opens no other, so it first goes back to the
 * person's own session — the one the refresh cookie holds — and asks from
 * there: one choice, two steps, and a refusal leaves them in their own view.
 * Off an Admin page, which a cast never opens, it lands where the team starts.
 */
export function useCastOnto() {
	const { castTeamId, roleView, enterRoleView, exitRoleView } = useAuth();
	const router = useRouter();
	const pathname = usePathname() ?? "";
	// `left` is told once the person is back in their own session, so a
	// refusal after it can say where they are, and one before it does not.
	return async (team: { id: string; name: string }, left?: () => void) => {
		if (team.id === castTeamId) return;
		if (castTeamId || roleView.length > 0) {
			await exitRoleView();
			left?.();
		}
		const opened = await sessions.openTeamCast({ teamId: team.id });
		enterRoleView(opened.bearer, `team ${team.name}`);
		if (pathname.startsWith("/admin")) router.push("/");
	};
}

/** The "View as team" dialog: the organization's teams, grouped by what they hold, searchable. */
export function TeamSwitcher() {
	const open = useTeamSwitcherOpen();
	const { castTeamId, exitRoleView } = useAuth();
	const { teams, loading, kept } = useTeamDirectory();
	const castOnto = useCastOnto();
	const { contains } = useCommandFilter();
	const [query, setQuery] = useState("");
	const [asking, setAsking] = useState("");
	const [refusal, setRefusal] = useState("");
	const close = () => {
		setTeamSwitcherOpen(false);
		setQuery("");
		setRefusal("");
	};
	const groups = new Map<
		string,
		{ team: TeamHoldings; role: string; installationId: string }[]
	>();
	for (const team of teams)
		for (const h of team.holds) {
			if (!contains(`${team.name} ${h.title} ${h.target} ${h.role}`, query))
				continue;
			groups.set(h.title, [
				...(groups.get(h.title) ?? []),
				{ team, role: h.role, installationId: h.installationId },
			]);
		}
	const choose = async (team: TeamHoldings) => {
		if (team.id === castTeamId) return close();
		setAsking(team.id);
		setRefusal("");
		let backInOwn = false;
		try {
			await castOnto(team, () => {
				backInOwn = true;
			});
			close();
			toast.success(`Viewing as ${team.name}`);
		} catch (error) {
			setRefusal(
				backInOwn
					? `You are back in your own view. The host did not open ${team.name}: ${castRefusal(error)}`
					: `Not viewing as ${team.name}: ${castRefusal(error)}`,
			);
		} finally {
			setAsking("");
		}
	};
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => (next ? setTeamSwitcherOpen(true) : close())}
		>
			<DialogContent className="top-[12vh] translate-y-0 sm:max-w-lg overflow-hidden p-0">
				<DialogHeader className="px-4 pt-4">
					<DialogTitle>View as a team</DialogTitle>
					<DialogDescription>
						See the product as a member of a team sees it: its pages and roles,
						none of yours. Your own session stays, and is one click back.
					</DialogDescription>
				</DialogHeader>
				<Command value={query} onValueChange={setQuery}>
					<CommandInput
						placeholder="Search teams, pages or roles…"
						aria-label="Search teams"
					/>
					<CommandList className="max-h-80">
						{groups.size === 0 && (
							<CommandEmpty>
								{loading
									? "Reading the organization's teams…"
									: query
										? "No team matches."
										: kept
											? "No teams were read in this tab before this view opened. Go back to your own view to list them."
											: "No team holds a role on anything installed, so there is no team to view as."}
							</CommandEmpty>
						)}
						{[...groups].map(([title, rows]) => (
							<CommandGroup key={title} heading={title}>
								{rows.map(({ team, role, installationId }) => (
									<CommandItem
										key={`${team.id}:${installationId}:${role}`}
										value={`${team.id}:${installationId}:${role}`}
										disabled={!!asking}
										onSelect={() => void choose(team)}
									>
										<UsersRound className="text-muted-foreground" />
										<span className="truncate font-medium">{team.name}</span>
										<span className="ml-auto flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
											{asking === team.id
												? "Opening…"
												: holdingLabel({ title, role })}
											{team.id === castTeamId && (
												<Check
													aria-label="Current"
													className="text-foreground"
												/>
											)}
										</span>
									</CommandItem>
								))}
							</CommandGroup>
						))}
					</CommandList>
				</Command>
				{refusal && (
					<p
						role="alert"
						className="mx-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm"
					>
						{refusal}
					</p>
				)}
				<div className="flex items-center justify-between gap-2 border-t px-4 py-3 text-xs text-muted-foreground">
					<span>The host decides whether you may view as a team.</span>
					{castTeamId && (
						<Button
							size="sm"
							variant="outline"
							onClick={() => void backToMine(exitRoleView).then(close)}
						>
							<Undo2 className="mr-1 h-4 w-4" /> Back to my view
						</Button>
					)}
				</div>
			</DialogContent>
		</Dialog>
	);
}

const backToMine = (exit: () => Promise<void>) =>
	exit().catch((e: unknown) => {
		toast.error(
			e instanceof Error ? e.message : "Could not restore your session.",
		);
	});

/** What a team holds, as badges, or that it holds nothing. */
export function HoldingBadges({ holds }: { holds: Holding[] }) {
	if (holds.length === 0)
		return (
			<span className="text-sm text-muted-foreground">
				Holds nothing installed
			</span>
		);
	return (
		<span className="flex flex-wrap gap-1">
			{holds.map((h) => (
				<Badge key={`${h.installationId}:${h.role}`} variant="secondary">
					{holdingLabel(h)}
				</Badge>
			))}
		</span>
	);
}

/**
 * What a cast holds, as the host answers it to the cast session itself
 * (ListHeld), named by the menu; until it answers, what this tab read of the
 * team before the cast.
 */
function useCastHoldings(
	castTeamId: string,
	organizationId: string,
): Holding[] {
	const titles = useTargetTitles();
	const held = useQuery({
		queryKey: ["cast-holdings", organizationId, castTeamId],
		enabled: !!castTeamId && !!organizationId,
		retry: false,
		queryFn: async () =>
			(await installations.listHeld({ organizationId })).installations,
	});
	if (held.data)
		return held.data.flatMap((h) =>
			h.roles.map((role) => {
				const target = h.installation?.target ?? "";
				return {
					installationId: h.installation?.id ?? "",
					target,
					title: titles.get(target) ?? target,
					role,
				};
			}),
		);
	return (
		keptDirectory(organizationId).find((t) => t.id === castTeamId)?.holds ?? []
	);
}

/**
 * The bar a narrowed session wears on every page: one calm line naming the
 * team (or the role view) and what it holds, the way to switch, the way back,
 * and the rest in a popover. It sits in the page, never over it.
 */
export function CastIndicator() {
	const { user, organizationId = "", exitRoleView } = useAuth();
	const { castTeamId, roleView, sessionRead } = useAuth();
	const { teams } = useTeamDirectory();
	const holds = useCastHoldings(castTeamId, organizationId);
	const roster = useQuery({
		...teamQueries.list(castTeamId ? organizationId : ""),
		retry: false,
	});
	if (sessionRead !== "confirmed" || (!castTeamId && roleView.length === 0))
		return null;
	const name =
		teams.find((t) => t.id === castTeamId)?.name ??
		roster.data?.teams.find((t) => t.id === castTeamId)?.name ??
		castTeamId;
	const viewed = roleView
		.map((v) => `${v.roles.join(", ")} on ${v.installationId}`)
		.join("; ");
	const summary = castTeamId ? holds.map(holdingLabel).join(", ") : viewed;
	return (
		<div
			role="status"
			aria-label={castTeamId ? `Viewing as team ${name}` : "Role view"}
			className="flex min-h-10 flex-wrap items-center gap-x-2 gap-y-1 border-b border-warning/40 bg-warning/10 px-4 py-1.5 text-sm"
		>
			{/* Who, then the actions: on a narrow screen the actions take the
			    second line rather than cut the team's name. */}
			<span className="flex min-w-0 flex-[1_1_16rem] items-center gap-2">
				<UsersRound className="h-4 w-4 shrink-0 text-warning" aria-hidden />
				<span className="min-w-0 truncate">
					{castTeamId ? (
						<>
							Viewing as <strong>{name}</strong>
						</>
					) : (
						<strong>Role view</strong>
					)}
					{summary && (
						<span className="hidden text-muted-foreground sm:inline">
							{" "}
							· {summary}
						</span>
					)}
				</span>
				<Popover>
					<PopoverTrigger
						render={
							<Button
								variant="ghost"
								size="sm"
								className="h-7 w-7 shrink-0 p-0"
								aria-label="What this view is"
							/>
						}
					>
						<Info className="h-4 w-4" />
					</PopoverTrigger>
					<PopoverContent align="start" className="w-80 space-y-2 text-sm">
						{castTeamId ? (
							<>
								<p>
									You are <strong>{user?.email ?? "signed in"}</strong>, viewing
									as team <strong>{name}</strong>. This view holds what the team
									holds and nothing of your own: not your roles, your other
									teams or your administrator access.
								</p>
								<HoldingBadges holds={holds} />
								<p className="text-muted-foreground">
									What you do here is attributed to the team. Data sources and
									scopes you hold personally are still read as you. A reload
									keeps this view in this tab only.
								</p>
							</>
						) : (
							<p>
								This session acts as {viewed}, and as everything you hold
								elsewhere. Your own session is the way back.
							</p>
						)}
					</PopoverContent>
				</Popover>
			</span>
			<span className="ml-auto flex shrink-0 items-center gap-1">
				{castTeamId && (
					<Button
						size="sm"
						variant="ghost"
						className="h-7"
						onClick={() => setTeamSwitcherOpen(true)}
					>
						Switch team
					</Button>
				)}
				<Button
					size="sm"
					variant="outline"
					className="h-7"
					onClick={() => void backToMine(exitRoleView)}
				>
					<Undo2 className="mr-1 h-4 w-4" />
					{castTeamId ? "Back to my view" : "Back to all my roles"}
				</Button>
			</span>
		</div>
	);
}
