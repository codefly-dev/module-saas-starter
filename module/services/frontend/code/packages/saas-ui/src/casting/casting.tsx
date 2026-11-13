"use client";

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
} from "@codefly-dev/ui/layout";
import { Check, Eye, Info, Undo2, UsersRound } from "lucide-react";
import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useState,
	useSyncExternalStore,
} from "react";
import { type CastHost, type CastSession, type CastTeam, type Holding, refusalText, signedOut } from "./host.js";
import {
	type CastRefusal,
	castAsk,
	forgetCastTeams,
	idleCastAsk,
	keepCastTeams,
	keptCastTeams,
	setCastAsk,
	subscribeCastAsk,
} from "./store.js";

/** Team casting as one tab sees it: the host's session, the teams to act as, and the acts. */
export interface Casting {
	/** The session the host answered; undefined while it is read or when it could not be. */
	session: CastSession | undefined;
	/** The teams to choose from: the host's list in the person's own view, what this tab kept of it during a cast. */
	teams: CastTeam[];
	/** The cast's own team, as the host lists it to the cast. */
	castTeam: CastTeam | undefined;
	loading: boolean;
	/** The list is what this tab kept before the cast opened. */
	kept: boolean;
	/** Opens a cast onto team: from a cast or role view, the way back first, then the ask. The refusal, or null once open. */
	ask(team: CastTeam): Promise<CastRefusal | null>;
	/** The way back to the person's own session. */
	back(): Promise<void>;
	/** The open dialog, what is being asked, and the last refusal. */
	open: boolean;
	asking: string;
	refusal: CastRefusal | null;
	backFailed: string;
}

// Outside a CastingProvider there is no host to ask: nothing is said, nothing is offered.
const NO_HOST: Casting = {
	session: undefined,
	teams: [],
	castTeam: undefined,
	loading: false,
	kept: false,
	open: false,
	asking: "",
	refusal: null,
	backFailed: "",
	ask: async (team) => ({ teamId: team.id, name: team.name, backInOwn: false, reason: "no host serves team casting here" }),
	back: async () => undefined,
};

const CastingContext = createContext<Casting>(NO_HOST);

/** Where the switcher was opened from, so focus goes back there when it closes. */
let opener: HTMLElement | null = null;

/** Opens the "View as a team" switcher from anywhere on the page. */
export function openTeamSwitcher() {
	opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
	setCastAsk({ open: true, refusal: null });
}

function closeTeamSwitcher() {
	setCastAsk({ open: false });
}

/**
 * Where focus goes when the switcher closes: where it was opened from, or,
 * when that is gone (a cast remounts the page), the bar's Switch team.
 */
function returnFocus(): HTMLElement | true {
	const back = opener?.isConnected ? opener : document.querySelector<HTMLElement>("[data-cast-switch]");
	opener = null;
	return back ?? true;
}

/**
 * Reads the session and the teams from host, and gives every casting control
 * below it the same state. `titleOf` names an installed target as the host's
 * menu does; the target itself when it has no name.
 */
export function CastingProvider({
	host,
	titleOf,
	children,
}: {
	host: CastHost;
	titleOf?: (target: string) => string | undefined;
	children: ReactNode;
}) {
	const ask = useSyncExternalStore(subscribeCastAsk, castAsk, idleCastAsk);
	const [generation, setGeneration] = useState(0);
	const reread = useCallback(() => setGeneration((g) => g + 1), []);
	// One answer per read, for the host and the read it answers: a new host (a
	// different person) is never shown the previous one's session.
	const [read, setRead] = useState<{ host: CastHost; generation: number; session?: CastSession; teams: CastTeam[] }>();
	useEffect(() => {
		let live = true;
		host.current().then(
			async (s) => {
				const teams = await host.listTeams().catch(() => [] as CastTeam[]);
				if (!live) return;
				setRead({ host, generation, session: s, teams });
			},
			(error: unknown) => {
				if (!live) return;
				setRead({ host, generation, teams: [] });
				// Signed out: nothing this tab kept belongs to the next person.
				if (signedOut(error)) forgetCastTeams();
			},
		);
		return () => {
			live = false;
		};
	}, [host, generation]);
	const ours = read?.host === host ? read : undefined;
	const session = ours?.session;
	const listed = ours?.teams;
	const reading = !ours || ours.generation !== generation;
	// A holding is named as the menu names its target, else as it was named
	// when it was kept: a cast's menu lists only its own pages, so a kept
	// team's other pages would otherwise be named by their ids.
	const named = useCallback(
		(teams: CastTeam[]) =>
			teams.map((t) => ({
				...t,
				holds: t.holds.map((h) => ({ ...h, title: titleOf?.(h.target) || h.title || h.target })),
			})),
		[titleOf],
	);
	// The person's own view keeps its list, named as its menu names each page,
	// for the cast that lists only itself. Only a current read is kept: while a
	// cast's read is in flight the previous own-view read is still held, and the
	// menu may already be the cast's.
	useEffect(() => {
		if (!reading && session && listed && !session.castTeamId && session.roleView.length === 0)
			keepCastTeams(session.personId, session.organizationId, named(listed));
	}, [reading, session, listed, named]);

	const value = useMemo<Casting>(() => {
		const narrowed = !!session && (!!session.castTeamId || session.roleView.length > 0);
		const kept = named(narrowed && !!session ? keptCastTeams(session.personId, session.organizationId) : []);
		const listed_ = listed && named(listed);
		const back = async () => {
			setCastAsk({ backFailed: "" });
			try {
				await host.back();
			} catch (error) {
				setCastAsk({ backFailed: refusalText(error) });
				return;
			}
			host.changed?.();
			reread();
		};
		return {
			session,
			teams: narrowed ? kept : (listed_ ?? []),
			castTeam: session?.castTeamId
				? (listed_?.find((t) => t.id === session.castTeamId) ?? kept.find((t) => t.id === session.castTeamId))
				: undefined,
			loading: reading,
			kept: narrowed,
			...ask,
			back,
			ask: async (team) => {
				if (team.id === session?.castTeamId) return null;
				setCastAsk({ asking: team.id, refusal: null });
				let backInOwn = false;
				try {
					if (narrowed) {
						try {
							await host.back();
						} catch (error) {
							throw new Error(`Could not go back to your own view: ${refusalText(error)}`);
						}
						backInOwn = true;
					}
					await host.open(team);
					setCastAsk({ asking: "", open: false, refusal: null });
					host.changed?.();
					reread();
					return null;
				} catch (error) {
					const reason = error instanceof Error && !("code" in error) ? error.message : refusalText(error);
					const refusal = { teamId: team.id, name: team.name, backInOwn, reason };
					setCastAsk({ asking: "", refusal });
					if (backInOwn) {
						host.changed?.();
						reread();
					}
					return refusal;
				}
			},
		};
	}, [host, named, session, listed, reading, ask, reread]);
	return <CastingContext.Provider value={value}>{children}</CastingContext.Provider>;
}

/** The casting state of the nearest CastingProvider; without one, a session nobody read and no team. */
export function useCasting(): Casting {
	return useContext(CastingContext);
}

/** "Directory · editor": one holding in the words the menu uses. */
export const holdingLabel = (h: Pick<Holding, "title" | "role">) => `${h.title} · ${h.role}`;

/** What a team holds, as badges, or that it holds nothing. */
export function HoldingBadges({ holds }: { holds: Holding[] }) {
	if (holds.length === 0) return <span className="type-body text-muted-foreground">Holds nothing installed</span>;
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

/** The refusal of the last ask, in the host's words, saying where the person now is. */
function Refusal({ refusal }: { refusal: CastRefusal }) {
	return (
		<p role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 type-body">
			{refusal.backInOwn
				? `You are back in your own view. ${refusal.name} was not opened: ${refusal.reason}`
				: `Not viewing as ${refusal.name}: ${refusal.reason}`}
		</p>
	);
}

/** The "View as a team" dialog: the teams the host lists, grouped by what they hold, searchable. */
export function TeamSwitcher() {
	const { open, asking, refusal, ask, back, teams, loading, kept, session } = useCasting();
	const { contains } = useCommandFilter();
	const [query, setQuery] = useState("");
	const close = () => {
		setQuery("");
		closeTeamSwitcher();
	};
	const groups = new Map<string, { team: CastTeam; role: string; installationId: string }[]>();
	for (const team of teams)
		for (const h of team.holds) {
			if (!contains(`${team.name} ${h.title} ${h.target} ${h.role}`, query)) continue;
			groups.set(h.title, [...(groups.get(h.title) ?? []), { team, role: h.role, installationId: h.installationId }]);
		}
	const castTeamId = session?.castTeamId ?? "";
	return (
		<Dialog open={open} onOpenChange={(next) => (next ? setCastAsk({ open: true }) : close())}>
			<DialogContent className="overflow-hidden p-0" finalFocus={returnFocus}>
				<DialogHeader className="p-4 pb-0">
					<DialogTitle>View as a team</DialogTitle>
					<DialogDescription>
						See the product as a member of a team sees it: its pages and roles, none of yours. Your own session
						stays, and is one click back.
					</DialogDescription>
				</DialogHeader>
				<Command value={query} onValueChange={setQuery}>
					<CommandInput placeholder="Search teams, pages or roles…" aria-label="Search teams" />
					<CommandList className="max-h-72">
						{groups.size === 0 && (
							<CommandEmpty>
								{loading
									? "Reading the teams you may view as…"
									: query
										? "No team matches."
										: kept
											? "No teams were read in this tab before this view opened. Go back to your own view to list them."
											: "There is no team you may view as: none holds a role you could grant."}
							</CommandEmpty>
						)}
						{[...groups].map(([title, rows]) => (
							<CommandGroup key={title} heading={title}>
								{rows.map(({ team, role, installationId }) => (
									<CommandItem
										key={`${team.id}:${installationId}:${role}`}
										value={`${team.id}:${installationId}:${role}`}
										disabled={!!asking}
										onSelect={() => void (team.id === castTeamId ? close() : ask(team))}
									>
										<UsersRound className="text-muted-foreground" />
										<span className="truncate type-emphasis">{team.name}</span>
										<span className="ml-auto flex shrink-0 items-center gap-2 type-caption-plain text-muted-foreground">
											{asking === team.id ? "Opening…" : holdingLabel({ title, role })}
											{team.id === castTeamId && <Check aria-label="Current" className="text-foreground" />}
										</span>
									</CommandItem>
								))}
							</CommandGroup>
						))}
					</CommandList>
				</Command>
				{refusal && (
					<div className="px-4">
						<Refusal refusal={refusal} />
					</div>
				)}
				<div className="flex items-center justify-between gap-2 border-t px-4 py-3 type-caption-plain text-muted-foreground">
					<span>The host decides whether you may view as a team.</span>
					{castTeamId && (
						<Button size="sm" variant="outline" onClick={() => void back().finally(close)}>
							<Undo2 className="mr-1 h-4 w-4" /> Back to my view
						</Button>
					)}
				</div>
			</DialogContent>
		</Dialog>
	);
}

/**
 * The bar a narrowed session wears on every page: one calm line naming the
 * team (or the role view) and what it holds, the way to switch, the way back,
 * and the rest in a popover. It sits in the page, never over it.
 */
export function CastBar() {
	const { session, castTeam, back, backFailed } = useCasting();
	if (!session || (!session.castTeamId && session.roleView.length === 0)) return null;
	const cast = !!session.castTeamId;
	const name = castTeam?.name ?? session.castTeamId;
	const holds = castTeam?.holds ?? [];
	const viewed = session.roleView.map((v) => `${v.roles.join(", ")} on ${v.installationId}`).join("; ");
	const summary = cast ? holds.map(holdingLabel).join(", ") : viewed;
	return (
		<section
			aria-label={cast ? "Team view" : "Role view"}
			className="flex min-h-9 flex-wrap items-center gap-2 border-b bg-warning/10 px-4 py-1.5 type-body"
		>
			{/* Who, then the actions: on a narrow screen the actions take the second line rather than cut the name. */}
			<span className="flex min-w-0 flex-1 items-center gap-2">
				<UsersRound className="h-4 w-4 shrink-0 text-warning" aria-hidden />
				{/* Only the announcement is live: the controls beside it are not re-read on every change. */}
				<span role="status" className="min-w-0 truncate">
					{cast ? (
						<>
							Viewing as <strong>{name}</strong>
						</>
					) : (
						<strong>Role view</strong>
					)}
					{summary && <span className="text-muted-foreground"> · {summary}</span>}
				</span>
				<Popover>
					<PopoverTrigger
						render={<Button variant="ghost" size="sm" className="shrink-0" aria-label="What this view is" />}
					>
						<Info className="h-4 w-4" />
					</PopoverTrigger>
					<PopoverContent align="start" className="flex w-72 flex-col gap-2 type-body">
						{cast ? (
							<>
								<p>
									You are <strong>{session.personDisplayName || "signed in"}</strong>, viewing as team{" "}
									<strong>{name}</strong>. This view holds what the team holds and nothing of your own: not your
									roles, your other teams or your administrator access.
								</p>
								<HoldingBadges holds={holds} />
								<p className="text-muted-foreground">
									What you do here is attributed to the team. A reload keeps this view in this tab only.
								</p>
							</>
						) : (
							<p>
								This session acts as {viewed}, and as everything you hold elsewhere. Your own session is the way
								back.
							</p>
						)}
					</PopoverContent>
				</Popover>
			</span>
			<span className="ml-auto flex shrink-0 items-center gap-1">
				{cast && (
					<Button size="sm" variant="ghost" data-cast-switch onClick={openTeamSwitcher}>
						Switch team
					</Button>
				)}
				<Button size="sm" variant="outline" onClick={() => void back()}>
					<Undo2 className="mr-1 h-4 w-4" />
					{cast ? "Back to my view" : "Back to all my roles"}
				</Button>
			</span>
			{backFailed && (
				<p role="alert" className="w-full text-muted-foreground">
					Could not go back to your own view: {backFailed}
				</p>
			)}
		</section>
	);
}

/**
 * "View as this team" for one team, as a row action or a card's control: the
 * one control that asks the host for a session that is this team and nothing
 * else, or the way back when it already is. Whether the person may is the
 * host's to decide; a refusal is shown in its words.
 */
export function ViewAsTeamButton({ team, label = "View as this team" }: { team: CastTeam; label?: string }) {
	const { session, asking, refusal, open, ask, back } = useCasting();
	if (session?.castTeamId === team.id)
		return (
			<Button size="sm" variant="outline" onClick={() => void back()}>
				<Undo2 className="mr-1 h-4 w-4" /> Back to my view
			</Button>
		);
	// The switcher says its own refusals; the control says the ones it was asked.
	const refused = !open && refusal?.teamId === team.id ? refusal : null;
	return (
		<span className="inline-flex flex-col items-end gap-2">
			<Button size="sm" disabled={asking === team.id} onClick={() => void ask(team)}>
				<Eye className="mr-1 h-4 w-4" />
				{asking === team.id ? "Opening…" : label}
			</Button>
			{refused && <Refusal refusal={refused} />}
		</span>
	);
}
