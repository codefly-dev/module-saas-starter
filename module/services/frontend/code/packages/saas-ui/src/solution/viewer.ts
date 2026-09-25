import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import type { SolutionRequestBinding } from "./binding.js";
import { solutionJson } from "./request.js";

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
	try {
		const body = token.split(".")[1] ?? "";
		const claims = JSON.parse(
			atob(body.replace(/-/g, "+").replace(/_/g, "/")),
		) as Record<string, unknown>;
		if (typeof claims.sub === "string" && claims.sub) {
			return JSON.stringify([
				claims.iss,
				claims.sub,
				claims.org,
				claims.auth_time,
				claims.acting,
				claims.act,
			]);
		}
	} catch {
		/* Opaque credentials cannot establish continuity across rotation. */
	}
	return token;
}

/**
 * The viewer's active organization as the access token names it (its `org`
 * claim), or "" when it names none. It selects the organization in requests
 * and partitions local state; it grants nothing, since every owner re-derives
 * the organization from the viewer's own authority.
 */
export function viewerOrganization(token: string | null | undefined): string {
	try {
		const body = (token ?? "").split(".")[1] ?? "";
		const claims = JSON.parse(
			atob(body.replace(/-/g, "+").replace(/_/g, "/")),
		) as { org?: unknown };
		return typeof claims.org === "string" ? claims.org : "";
	} catch {
		return "";
	}
}

/**
 * The host's current access token, observed. The host's getter is stable while
 * its token store changes underneath it, so the value is re-read on the host's
 * `codefly:auth-changed` event, on focus, on storage, and on a short interval —
 * a change that neither replaces the getter nor rerenders the host still
 * reaches the remote.
 */
export function useAccessToken(
	getAccessToken?: () => string | null,
): string | null {
	const subscribe = useCallback((changed: () => void) => {
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
	}, []);
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
export function useViewerEpoch(getAccessToken?: () => string | null): number {
	const identity = viewerIdentity(useAccessToken(getAccessToken));
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
	const { apiBase, getAccessToken, authedFetch } = binding;
	const viewer = viewerIdentity(useAccessToken(getAccessToken));
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
		solutionJson<T>({ apiBase, getAccessToken, authedFetch }, path, {
			signal: controller.signal,
			cache,
		})
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
