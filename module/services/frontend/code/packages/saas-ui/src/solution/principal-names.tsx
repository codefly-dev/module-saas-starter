"use client";

import { DirectoryService } from "@codefly-dev/saas-sdk";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
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
import {
	requestBinding,
	type SolutionBinding,
	type SolutionRequestBinding,
} from "./binding.js";
import { solutionTransport } from "./transport.js";
import {
	useAccessToken,
	viewerIdentity,
	viewerOrganization,
	viewerPrincipal,
} from "./viewer.js";

/**
 * What a reader calls a principal, from the directory the viewer may read.
 *
 * `display` is the directory's label for the member. The host's tenant
 * directory carries a member's email and nothing else, so today `display` IS
 * the email; it is its own field so a richer directory changes what a reader
 * sees without changing a consumer. `you` is an exact match with the principal
 * reading (`viewerPrincipal`), never a near one.
 */
export interface PrincipalName {
	display: string;
	email?: string;
	avatarUrl?: string;
	you: boolean;
}

/**
 * The function form a module kit takes as a prop: the directory's label for a
 * principal, or `undefined` when the directory is still loading, refused, or
 * does not list that principal. It never answers "You" — a kit decides that
 * from the viewer it is given, exactly — and never answers an empty string.
 */
export type NameOf = (principal: string) => string | undefined;

/**
 * Where the directory read stands, said once for the whole page rather than
 * once per name. `refused` is a viewer who may not read the directory (the
 * host answered permission denied, the credential names no organization, or
 * there is no credential at all) and is kept for that viewer. `failed` is
 * anything else; it stays `failed` while a retry is in flight, and when one is
 * made follows `Retry` below.
 */
export type PrincipalDirectoryState =
	| { status: "loading" }
	| { status: "ready" }
	| { status: "refused"; reason: string }
	| { status: "failed"; reason: string };

interface Listed {
	display: string;
	email: string;
}

/**
 * When a failed read may be tried again: after a backoff (a gateway or network
 * failure that can heal by itself), only once the host hands over a new token
 * (the credential was not accepted), or not for this viewer at all (the
 * request itself is wrong — a malformed organization, a missing route).
 */
type Retry = "backoff" | "token" | "never";

/** The directory as the store holds it: the public state plus, when ready, the members. */
type Held =
	| { status: "loading" }
	| { status: "ready"; members: ReadonlyMap<string, Listed> }
	| { status: "refused"; reason: string }
	| { status: "failed"; reason: string; retry: Retry };

const LOADING: Held = { status: "loading" };
const READY: PrincipalDirectoryState = { status: "ready" };
const NO_CREDENTIAL: Held = { status: "refused", reason: "no credential" };

/** How long a transient failure is kept before a read is tried again. */
const BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 60_000;

/** The state of one viewer's read: what it holds and when it may be read again. */
interface Entry {
	viewer: string;
	held: Held;
	inFlight: boolean;
	failures: number;
	retryAt: number;
	/** The token a "token" failure was answered for. */
	token: string | null;
}

/**
 * One viewer's directory, read at most once for as long as that viewer is the
 * viewer. It holds a single entry: a different viewer replaces it, so no name
 * one viewer was shown survives into the next viewer's page, and an answer for
 * a viewer who is no longer current is dropped instead of stored.
 */
class DirectoryStore {
	private entry: Entry | null = null;
	private readonly listeners = new Set<() => void>();

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	held(viewer: string | null): Held {
		if (viewer === null) return NO_CREDENTIAL;
		return this.entry?.viewer === viewer ? this.entry.held : LOADING;
	}

	/**
	 * Starts the read for `viewer` unless one is already held or in flight for
	 * them. Every component on the page asks; the first one's request is the
	 * only one sent, and the rest share its answer.
	 *
	 * A failure is kept, and shown, until its retry rule allows another read:
	 * a retry never turns the page back to loading, so a page that renders
	 * names only once the directory settled cannot unmount and remount its way
	 * into a request loop. `current` re-reads who the viewer is when the answer
	 * arrives; an answer for a viewer who is no longer current is dropped, even
	 * before the page has noticed the change.
	 */
	load(
		viewer: string,
		token: string,
		read: () => Promise<Held>,
		current: () => string | null,
	): void {
		let entry = this.entry;
		if (entry?.viewer !== viewer) {
			const replaced = entry;
			entry = {
				viewer,
				held: LOADING,
				inFlight: false,
				failures: 0,
				retryAt: 0,
				token: null,
			};
			this.entry = entry;
			if (replaced && replaced.held !== LOADING) this.emit();
		}
		if (entry.inFlight || !this.mayRead(entry, token)) return;
		const started = entry;
		started.inFlight = true;
		void read().then((held) => {
			if (this.entry !== started) return;
			started.inFlight = false;
			if (current() !== viewer) return;
			if (held.status === "failed") {
				started.failures += 1;
				started.token = token;
				started.retryAt =
					held.retry === "backoff"
						? Date.now() +
							Math.min(BACKOFF_MS * 2 ** (started.failures - 1), MAX_BACKOFF_MS)
						: Number.POSITIVE_INFINITY;
			} else started.failures = 0;
			started.held = held;
			this.emit();
		});
	}

	private mayRead(entry: Entry, token: string): boolean {
		const { held } = entry;
		if (held.status === "loading") return true;
		if (held.status !== "failed") return false;
		if (held.retry === "token") return token !== entry.token;
		return Date.now() >= entry.retryAt;
	}

	private emit(): void {
		for (const listener of this.listeners) listener();
	}
}

/**
 * How a failed read may be retried, by its code. A refused authority
 * (permission denied) is not a failure at all; it is returned as `refused`.
 */
function retryOf(code: Code): Retry {
	switch (code) {
		case Code.Unauthenticated:
			return "token";
		case Code.InvalidArgument:
		case Code.NotFound:
		case Code.Unimplemented:
		case Code.FailedPrecondition:
			return "never";
		default:
			return "backoff";
	}
}

/**
 * One read of the viewer's organization directory, as the store holds it. A
 * member row naming any organization other than the one asked about is
 * ignored rather than trusted, and a member the directory gives no label
 * (a deleted or unavailable account) is left unknown rather than named "".
 */
async function readDirectory(
	binding: () => SolutionRequestBinding,
	org: string,
): Promise<Held> {
	if (!org)
		return {
			status: "refused",
			reason: "the viewer's credential names no organization",
		};
	try {
		const { members } = await createClient(
			DirectoryService,
			solutionTransport(binding()),
		).listOrganizationMembers({ orgId: org });
		const listed = new Map<string, Listed>();
		for (const member of members) {
			if (member.orgId !== org || !member.userId || !member.userEmail) continue;
			listed.set(member.userId, {
				display: member.userEmail,
				email: member.userEmail,
			});
		}
		return { status: "ready", members: listed };
	} catch (failure) {
		const error = ConnectError.from(failure);
		return error.code === Code.PermissionDenied
			? { status: "refused", reason: error.rawMessage || error.message }
			: { status: "failed", reason: error.message, retry: retryOf(error.code) };
	}
}

interface DirectoryContext {
	store: DirectoryStore;
	viewer: string | null;
	principal: string;
	load: () => void;
}

const Directory = createContext<DirectoryContext | null>(null);

export interface PrincipalNamesProviderProps {
	/**
	 * The host binding. `getAccessToken` is required here even though a request
	 * binding may go without it: the names are kept per viewer, and a binding
	 * that cannot say who the viewer is cannot keep them apart.
	 */
	binding: SolutionRequestBinding & Pick<SolutionBinding, "getAccessToken">;
	children?: ReactNode;
}

/**
 * Holds the viewer's organization directory for every name rendered beneath
 * it: one `DirectoryService.ListOrganizationMembers` read per viewer, made the
 * first time something beneath asks and kept until the viewer changes (the
 * same change `useViewerEpoch` counts). Mount it once, around the page.
 *
 * The read is the viewer's own: the host answers only for an organization the
 * viewer belongs to, so a principal outside it is simply not listed and
 * resolves to `undefined` — never to another tenant's member.
 */
export function PrincipalNamesProvider({
	binding,
	children,
}: PrincipalNamesProviderProps) {
	const { apiBase, getAccessToken, authedFetch, subscribeToken } = binding;
	const token = useAccessToken(getAccessToken, subscribeToken);
	const viewer = viewerIdentity(token);
	const principal = viewerPrincipal(token);
	const org = viewerOrganization(token);
	const [store] = useState(() => new DirectoryStore());
	const load = useCallback(() => {
		if (viewer === null || token === null) return;
		store.load(
			viewer,
			token,
			() =>
				readDirectory(
					() => requestBinding(apiBase, getAccessToken, authedFetch),
					org,
				),
			() => viewerIdentity(getAccessToken()),
		);
	}, [store, viewer, token, org, apiBase, getAccessToken, authedFetch]);
	const value = useMemo(
		() => ({ store, viewer, principal, load }),
		[store, viewer, principal, load],
	);
	return <Directory.Provider value={value}>{children}</Directory.Provider>;
}

function useDirectory(): { held: Held; principal: string } {
	const context = useContext(Directory);
	if (!context)
		throw new Error(
			"principal names need a <PrincipalNamesProvider binding={…}> above them",
		);
	const { store, viewer, principal, load } = context;
	const held = useSyncExternalStore(
		store.subscribe,
		() => store.held(viewer),
		() => LOADING,
	);
	useEffect(() => {
		load();
	}, [load]);
	return { held, principal };
}

function nameIn(
	held: Held,
	principal: string,
	viewer: string,
): PrincipalName | undefined {
	if (held.status !== "ready") return undefined;
	const listed = held.members.get(principal);
	if (!listed) return undefined;
	return {
		display: listed.display,
		email: listed.email,
		you: viewer !== "" && principal === viewer,
	};
}

/**
 * The names of `principals`, from the viewer's directory: each one's
 * `PrincipalName`, or `undefined` while the directory loads, when it was
 * refused, or when it does not list that principal. Whether it is loading or
 * refused is `usePrincipalDirectory`'s to say, once for the page.
 *
 * `you` is set only on a listed principal. The viewer is `undefined` here
 * whenever the directory does not list them — while it loads, when it was
 * refused, or when an administrator views as someone outside it — so a caller
 * deciding "You" compares against `viewerPrincipal` itself, as
 * `<PrincipalName>` does.
 *
 * Any number of callers on a page share one directory read; asking again
 * after a re-render, or from another component, asks nobody.
 */
export function usePrincipalNames(
	principals: readonly string[],
): Record<string, PrincipalName | undefined> {
	const { held, principal: viewer } = useDirectory();
	const key = JSON.stringify(principals);
	return useMemo(() => {
		// No prototype, so a principal spelled like an Object method is not
		// answered by one.
		const names: Record<string, PrincipalName | undefined> =
			Object.create(null);
		for (const principal of JSON.parse(key) as string[])
			names[principal] = nameIn(held, principal, viewer);
		return names;
	}, [held, viewer, key]);
}

/** `NameOf` over the viewer's directory, for a kit that takes one as a prop. */
export function useNameOf(): NameOf {
	const { held } = useDirectory();
	return useCallback(
		(principal: string) =>
			held.status === "ready"
				? held.members.get(principal)?.display
				: undefined,
		[held],
	);
}

/** Where the page's one directory read stands. */
export function usePrincipalDirectory(): PrincipalDirectoryState {
	const { held } = useDirectory();
	return useMemo(() => {
		if (held.status === "ready") return READY;
		if (held.status === "failed")
			return { status: "failed", reason: held.reason };
		return held;
	}, [held]);
}

/** The id, shortened to its ends, for a principal no name is known for. */
function shortId(principal: string): string {
	return principal.length <= 12
		? principal
		: `${principal.slice(0, 4)}…${principal.slice(-4)}`;
}

export interface PrincipalNameProps {
	principal: string;
	className?: string;
}

/**
 * One principal on screen. "You" on an exact match with the principal
 * reading; the directory's label once it is known; otherwise — loading,
 * refused, or not listed — the id shortened to its ends. The full id is always
 * the `title`, and `data-state` says which of those it is (`none` when no
 * principal was recorded at all), so a reader can
 * always get from the words back to the id and never sees only the raw id
 * once a name is known.
 */
export function PrincipalName({ principal, className }: PrincipalNameProps) {
	const { held, principal: viewer } = useDirectory();
	if (!principal)
		return (
			<span
				className={className}
				title="No principal is recorded"
				data-state="none"
			>
				someone
			</span>
		);
	if (viewer !== "" && principal === viewer)
		return (
			<span
				className={className}
				title={principal}
				data-principal={principal}
				data-state="you"
			>
				You
			</span>
		);
	const name = nameIn(held, principal, viewer);
	const state = name
		? "named"
		: held.status === "ready"
			? "unknown"
			: held.status;
	return (
		<span
			className={className}
			title={principal}
			data-principal={principal}
			data-state={state}
			aria-busy={state === "loading" || undefined}
		>
			{name ? name.display : shortId(principal)}
		</span>
	);
}
