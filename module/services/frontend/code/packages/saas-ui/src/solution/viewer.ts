"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { requestBinding, type SolutionRequestBinding } from "./binding.js";
import { solutionJson } from "./request.js";

/**
 * The access token's claims, unverified, or null when they cannot be read at
 * all. ONE decoder for every reader below: three hand-rolled copies of this
 * base64url dance is three places for the padding, the URL alphabet or the
 * failure behaviour to drift apart, and a reader that drifted would disagree
 * with its neighbours about who the viewer is.
 *
 * Nothing here authorizes anything — the signature is never checked, so a claim
 * read through this only ever decides what a page offers or how local state is
 * partitioned.
 */
function unverifiedClaims(
	token: string | null | undefined,
): Record<string, unknown> | null {
	try {
		const body = (token ?? "").split(".")[1] ?? "";
		return JSON.parse(
			atob(body.replace(/-/g, "+").replace(/_/g, "/")),
		) as Record<string, unknown>;
	} catch {
		return null;
	}
}

/**
 * Who the current credential speaks for, as a stable key: the issuer, subject,
 * organization, original authentication time and acting chain of the access
 * token's claims. The host rotates the token (and its session id) on refresh;
 * this key stays the same across a refresh for the same authenticated viewer
 * and changes when the viewer does.
 *
 * It partitions local UI state — a transcript, a cache — by viewer and NEVER
 * authorizes anything: the claims are read without verification. A credential
 * whose claims cannot be read is its own key, so an opaque one establishes no
 * continuity across rotation.
 */
export function viewerIdentity(token: string | null): string | null {
	if (!token) return null;
	const claims = unverifiedClaims(token);
	if (claims && typeof claims.sub === "string" && claims.sub) {
		return JSON.stringify([
			claims.iss,
			claims.sub,
			claims.org,
			claims.auth_time,
			claims.acting,
			claims.act,
		]);
	}
	// Opaque credentials cannot establish continuity across rotation, so one is
	// its own key.
	return token;
}

/**
 * The viewer's active organization as the access token names it (its `org`
 * claim), or "" when it names none. It selects the organization in requests
 * and partitions local state; it grants nothing, since every owner re-derives
 * the organization from the viewer's own authority.
 */
export function viewerOrganization(token: string | null | undefined): string {
	const org = unverifiedClaims(token)?.org;
	return typeof org === "string" ? org : "";
}

/**
 * Whether the access token names the viewer an administrator of its active
 * organization — its owner or an admin (`or` claim), or a platform super
 * administrator (`pr` claim) — the tier the host requires to connect, sync or
 * remove a data source and to grant read access to a collection.
 *
 * Read without verification, like every claim here: it decides only what a
 * page OFFERS (a control, a link to where grants are made), never what is
 * allowed. The host refuses a non-administrator's call whatever this says.
 */
export function viewerAdministersOrganization(
	token: string | null | undefined,
): boolean {
	const claims = unverifiedClaims(token);
	return (
		claims?.or === "owner" ||
		claims?.or === "admin" ||
		claims?.pr === "super_admin"
	);
}

/**
 * Whether the access token says it expires within `marginMs` of `now` (its
 * `exp` claim), or has already expired. A token whose claims cannot be read, or
 * that carries no numeric `exp`, says nothing, and that is reported as false:
 * only the server can judge such a credential, so the caller should send it.
 *
 * Read without verification: it only decides whether to refresh BEFORE sending
 * a request that would otherwise be refused, never whether a request is allowed.
 */
export function accessTokenExpiresWithin(
	token: string | null | undefined,
	marginMs: number,
	now: number = Date.now(),
): boolean {
	const exp = unverifiedClaims(token)?.exp;
	return typeof exp === "number" && exp * 1000 - now <= marginMs;
}

/**
 * The host's current access token, observed. The getter stays stable while the
 * token store changes underneath it, so a change that neither replaces the
 * getter nor rerenders the host still has to reach the remote.
 *
 * `subscribeToken` is how the host says it will tell us, and then telling us is
 * all that happens. Without it there is nothing to be told by, so the value is
 * re-read on the host's `codefly:auth-changed` event, on focus, on storage —
 * and, because none of those is guaranteed to exist, on a short interval as a
 * last resort. That interval is one timer per observer that runs for as long as
 * the page is open, which is the whole reason a host should pass a subscription.
 */
export function useAccessToken(
	getAccessToken?: () => string | null,
	subscribeToken?: (listener: () => void) => () => void,
): string | null {
	const subscribe = useCallback(
		(changed: () => void) => {
			if (subscribeToken) return subscribeToken(changed);
			const timer = window.setInterval(changed, 250);
			window.addEventListener("codefly:auth-changed", changed);
			window.addEventListener("focus", changed);
			window.addEventListener("storage", changed);
			return () => {
				window.clearInterval(timer);
				window.removeEventListener("codefly:auth-changed", changed);
				window.removeEventListener("focus", changed);
				window.removeEventListener("storage", changed);
			};
		},
		[subscribeToken],
	);
	const snapshot = useCallback(
		() => getAccessToken?.() ?? null,
		[getAccessToken],
	);
	return useSyncExternalStore(subscribe, snapshot, () => null);
}

/**
 * A counter that advances each time the viewer changes (see `viewerIdentity`).
 * Keying a remote's page on it drops every piece of one viewer's state before
 * the next viewer's first render.
 */
export function useViewerEpoch(
	getAccessToken?: () => string | null,
	subscribeToken?: (listener: () => void) => () => void,
): number {
	const identity = viewerIdentity(
		useAccessToken(getAccessToken, subscribeToken),
	);
	const [current, setCurrent] = useState({ identity, epoch: 0 });
	if (current.identity !== identity) {
		const next = { identity, epoch: current.epoch + 1 };
		setCurrent(next);
		return next.epoch;
	}
	return current.epoch;
}

export type SolutionResource<T> =
	| { status: "loading" }
	| { status: "error"; message: string }
	| { status: "ready"; data: T };

/**
 * Reads one JSON resource from the solution's own backend and re-reads it when
 * the viewer, the base or the path changes. An answer is kept only if the
 * viewer who asked is still the viewer — a response that arrives after a
 * switch is dropped, never shown to the next person.
 */
export function useSolutionJson<T>(
	binding: SolutionRequestBinding,
	path: string,
	cache?: RequestCache,
): SolutionResource<T> {
	const { apiBase, getAccessToken, authedFetch, subscribeToken } = binding;
	const viewer = viewerIdentity(useAccessToken(getAccessToken, subscribeToken));
	const [result, setResult] = useState<{
		viewer: string | null;
		apiBase: string;
		path: string;
		state: SolutionResource<T>;
	} | null>(null);

	useEffect(() => {
		const controller = new AbortController();
		const settle = (state: SolutionResource<T>) => {
			if (
				!controller.signal.aborted &&
				viewerIdentity(getAccessToken?.() ?? null) === viewer
			)
				setResult({ viewer, apiBase, path, state });
		};
		// Built inside the chain, so a binding with no credential settles as an
		// error instead of throwing out of the effect.
		Promise.resolve()
			.then(() =>
				solutionJson<T>(
					requestBinding(apiBase, getAccessToken, authedFetch),
					path,
					{
						signal: controller.signal,
						cache,
					},
				),
			)
			.then((data) => settle({ status: "ready", data }))
			.catch((error: unknown) =>
				settle({ status: "error", message: String(error) }),
			);
		return () => controller.abort();
	}, [apiBase, path, cache, getAccessToken, authedFetch, viewer]);

	return result &&
		result.viewer === viewer &&
		result.apiBase === apiBase &&
		result.path === path
		? result.state
		: { status: "loading" };
}
